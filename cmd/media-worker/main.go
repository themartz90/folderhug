package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/themartz90/folderhug/internal/mediaworker"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var err error
	if len(os.Args) == 2 && os.Args[1] == "network-check" {
		err = mediaworker.NetworkCheck(ctx)
		if err == nil {
			fmt.Println("Media worker network isolation check passed.")
		}
	} else if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: media-worker [network-check]")
		os.Exit(2)
	} else {
		err = mediaworker.Run(ctx, mediaworker.Config{SocketPath: os.Getenv("HTTPWRAPPER_MEDIA_SOCKET"), KeyPath: os.Getenv("HTTPWRAPPER_WORKER_KEY"), FFmpegPath: os.Getenv("FFMPEG_PATH")})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
