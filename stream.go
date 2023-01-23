package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/g8rswimmer/go-twitter/v2"
	"golang.org/x/oauth2"

	"github.com/Ukraine-DAO/twitter-monitor/config"
)

const maxRuleLength = 512

func filterRulesFromIDs(ids []string) []string {
	r := []string{}
	cur := []string{}
	l := 0
	for _, id := range ids {
		s := "from:" + id
		// total length of strings in cur + length of s + length of "OR"s to be added
		ruleLen := l + len(s) + len(cur)*len(" OR ")
		if ruleLen > maxRuleLength {
			r = append(r, strings.Join(cur, " OR "))
			cur = nil
			l = 0
		}
		cur = append(cur, s)
		l += len(s)
	}
	if len(cur) > 0 {
		r = append(r, strings.Join(cur, " OR "))
	}
	return r
}

func stringify(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%#v", v)
	}
	return string(b)
}

func appTwitterClient(creds TwitterCredentials) *twitter.Client {
	return &twitter.Client{
		Authorizer: authorizer{oauth2.StaticTokenSource(&oauth2.Token{AccessToken: creds.BearerToken})},
		Client:     http.DefaultClient,
		Host:       "https://api.twitter.com",
	}
}

func setupFilterRules(ctx context.Context, client *twitter.Client, ids []string, extra []twitter.TweetSearchStreamRule) error {
	cfgRules := filterRulesFromIDs(ids)
	ruleMap := map[twitter.TweetSearchStreamRule]bool{}
	for _, r := range cfgRules {
		ruleMap[twitter.TweetSearchStreamRule{Value: r}] = true
	}
	for _, r := range extra {
		ruleMap[r] = true
	}

	rules, err := client.TweetSearchStreamRules(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to fetch existing filter rules: %s")
	}
	toDelete := []twitter.TweetSearchStreamRuleID{}
	for _, rule := range rules.Rules {
		if !ruleMap[rule.TweetSearchStreamRule] {
			toDelete = append(toDelete, rule.ID)
			continue
		}
		delete(ruleMap, rule.TweetSearchStreamRule)
	}
	if len(toDelete) > 0 {
		_, err := client.TweetSearchStreamDeleteRuleByID(ctx, toDelete, false)
		if err != nil {
			return fmt.Errorf("failed to delete unneeded rules: %s", err)
		}
	}
	if len(ruleMap) > 0 {
		req := []twitter.TweetSearchStreamRule{}
		for r := range ruleMap {
			req = append(req, r)
		}
		_, err := client.TweetSearchStreamAddRule(ctx, req, false)
		if err != nil {
			return fmt.Errorf("adding filter rule: %w", err)
		}
	}
	return nil
}

func updateFilterRules(ctx context.Context, cfg *config.Config, client *twitter.Client) error {
	idToChannels, err := cfg.TwitterIDToChannels()
	if err != nil {
		return fmt.Errorf("failed to map twitter IDs to Discord channels: %w", err)
	}
	twIDs := []string{}
	for id := range idToChannels {
		twIDs = append(twIDs, id)
	}
	sort.Strings(twIDs)

	taggedRules := []twitter.TweetSearchStreamRule{}
	for _, ch := range cfg.Channels {
		if ch.SearchQuery != "" {
			taggedRules = append(taggedRules, twitter.TweetSearchStreamRule{Value: ch.SearchQuery, Tag: ch.DiscordID})
		}
	}

	if err := setupFilterRules(ctx, client, twIDs, taggedRules); err != nil {
		return fmt.Errorf("failed to set up filter rules: %w", err)
	}

	return nil
}

func runStream(cfg *config.Config, discord *discordgo.Session) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	creds, err := creds(ctx)
	if err != nil {
		log.Fatalf("Failed to get credentials: %s", err)
	}

	idToChannels, err := cfg.TwitterIDToChannels()
	if err != nil {
		log.Fatalf("Failed to map twitter IDs to Discord channels: %s", err)
	}

	searchClient := appTwitterClient(creds.Twitter)
	if err := updateFilterRules(ctx, cfg, searchClient); err != nil {
		log.Fatalf("Failed to set up filter rules: %s", err)
	}

	cfgUpdateTicker := time.NewTicker(4 * time.Hour)
	defer cfgUpdateTicker.Stop()

	go func() {
		select {
		case <-ctx.Done():
			return
		case <-cfgUpdateTicker.C:
			if err := updateFilterRules(ctx, cfg, searchClient); err != nil {
				log.Printf("Failed to update filter rules: %s", err)
			}
		}
	}()

	for {
		stream, err := searchClient.TweetSearchStreamV2(ctx, twitter.TweetSearchStreamOpts{
			Expansions: []twitter.Expansion{
				twitter.ExpansionAuthorID,
				twitter.ExpansionReferencedTweetsID,
				twitter.ExpansionReferencedTweetsIDAuthorID,
			},
			TweetFields: []twitter.TweetField{
				twitter.TweetFieldID,
				twitter.TweetFieldText,
				twitter.TweetFieldAuthorID,
				twitter.TweetFieldReferencedTweets,
			},
			UserFields: []twitter.UserField{twitter.UserFieldUserName},
		})
		if err != nil {
			log.Printf("Failed to start stream: %s", err)
			time.Sleep(30 * time.Second)
			continue
		}
		log.Printf("Stream started")

		err = stream.Run(ctx, twitter.TweetStreamV2Options{
			OnTweet: func(t *twitter.StreamedTweet) {
				go func(t *twitter.StreamedTweet) {
					text := textForTweet(t.Tweet, t.Includes)
					for _, ch := range idToChannels[t.Tweet.AuthorID] {
						if _, err := discord.ChannelMessageSend(ch, text); err != nil {
							log.Printf("Failed to post %q to Discord: %s", text, err)
						}
					}

					for _, rule := range t.MatchingRules {
						if rule.Tag == "" {
							continue
						}
						if _, err := discord.ChannelMessageSend(rule.Tag, text); err != nil {
							log.Printf("Failed to post %q to Discord: %s", text, err)
						}
					}
				}(t)
			},
			OnSystemMessage: func(kind twitter.SystemMessageType, msg *twitter.SystemMessage) {
				log.Printf("System message: [%s] %#v", kind, msg)
			},
			OnTransientError: func(err error) {
				log.Printf("Transient stream error: %s", err)
			},
		})

		if ctx.Err() != nil {
			log.Printf("Context done, exiting")
			return
		}

		if err != nil {
			log.Printf("Stream has closed with an error: %s", err)
		}
	}
}

func textForTweet(tw *twitter.TweetObj, includes *twitter.TweetRawIncludes) string {
	authorUsername := func(tw *twitter.TweetObj) string {
		for _, u := range includes.Users {
			if u.ID == tw.AuthorID {
				return u.UserName
			}
		}
		return "i"
	}

	for _, ref := range tw.ReferencedTweets {
		if ref.Type == "retweeted" {
			for _, it := range includes.Tweets {
				if it.ID == ref.ID {
					return fmt.Sprintf("`@%s` retweeted https://twitter.com/%s/status/%s", authorUsername(tw), authorUsername(it), it.ID)
				}
			}
		}
	}

	return fmt.Sprintf("https://twitter.com/%s/status/%s", authorUsername(tw), tw.ID)
}
