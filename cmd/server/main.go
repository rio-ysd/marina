package main

import (
	"log"
	"net/http"
	"os"

	"github.com/yoshida-rio/marina/internal/app"
	"github.com/yoshida-rio/marina/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	a, err := app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}
	defer a.DB.Close()

	mux := a.Mux()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("marina server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
