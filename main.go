package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"

	"github.com/bwmarrin/discordgo"
	"golang.org/x/oauth2"
	runtimeconfig "google.golang.org/api/runtimeconfig/v1beta1"
	"google.golang.org/appengine/v2"

	"github.com/Ukraine-DAO/twitter-monitor/config"
)

type Credentials struct {
	Twitter TwitterCredentials
	Discord DiscordCredentials
}

type TwitterCredentials struct {
	BearerToken string
}

type DiscordCredentials struct {
	Token     string
	AppID     string
	PublicKey string
}

type authorizer struct {
	oauth2.TokenSource
}

func (a authorizer) Add(req *http.Request) {
	t, err := a.Token()
	if err != nil {
		log.Printf("Failed to get auth token: %s", err)
		return
	}
	t.SetAuthHeader(req)
}

func credsFromRuntimeConfig(ctx context.Context) (Credentials, error) {
	rcService, err := runtimeconfig.NewService(ctx)
	if err != nil {
		return Credentials{}, err
	}
	vars := rcService.Projects.Configs.Variables
	r := Credentials{}
	fields := []struct {
		name string
		dest *string
	}{
		{"twitter/bearer_token", &r.Twitter.BearerToken},
		{"discord/token", &r.Discord.Token},
		{"discord/app_id", &r.Discord.AppID},
		{"discord/public_key", &r.Discord.PublicKey},
	}
	for _, f := range fields {
		v, err := vars.Get(fmt.Sprintf("projects/%s/configs/prod/variables/%s",
			os.Getenv("GOOGLE_CLOUD_PROJECT"),
			url.PathEscape(f.name))).
			Do()
		if err != nil {
			return Credentials{}, fmt.Errorf("getting variable %q: %w", f.name, err)
		}
		*f.dest = v.Text
	}
	return r, nil
}

func credsFromEnv() Credentials {
	r := Credentials{}
	vars := []struct {
		name string
		dest *string
	}{
		{"TWITTER_BEARER_TOKEN", &r.Twitter.BearerToken},
		{"DISCORD_TOKEN", &r.Discord.Token},
		{"APP_ID", &r.Discord.AppID},
		{"PUBLIC_KEY", &r.Discord.PublicKey},
	}
	for _, v := range vars {
		*v.dest = os.Getenv(v.name)
	}
	return r
}

func creds(ctx context.Context) (Credentials, error) {
	if appengine.IsAppEngine() {
		return credsFromRuntimeConfig(ctx)
	}
	return credsFromEnv(), nil
}

func main() {
	configPath := os.Getenv("CONFIG_PATH")
	if configPath == "" {
		log.Fatalf("CONFIG_PATH needs to be set")
	}
	cfg, err := config.FromFile(configPath)
	if err != nil {
		log.Fatalf("Failed to read config: %s", err)
	}

	ctx := context.Background()
	creds, err := creds(ctx)
	if err != nil {
		log.Fatalf("Failed to get credentials: %s", err)
	}

	http.HandleFunc("/_ah/warmup", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
	}

	discord, err := discordgo.New("Bot " + creds.Discord.Token)
	if err != nil {
		log.Fatalf("Failed to create Discord client: %s")
	}

	go func() {
		log.Printf("Listening on port %s", port)
		if err := http.ListenAndServe(":"+port, nil); err != nil {
			log.Fatal(err)
		}
	}()

	runStream(cfg, discord)
}
