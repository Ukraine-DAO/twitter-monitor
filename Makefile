.PHONY: all build run

all: build

build: twitter-monitor

twitter-monitor: $(wildcard *.go go.*)
	go build .

run:
	. ./.env; go run .
