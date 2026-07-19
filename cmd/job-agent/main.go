package main

import (
	"fmt"
	"log"
	"os"

	"github.com/Darkon13/job-agent/adapter"
	"github.com/Darkon13/job-agent/adapters/hh"
	appconfig "github.com/Darkon13/job-agent/config"
	storesqlite "github.com/Darkon13/job-agent/storage/sqlite"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatalf("usage: %s <config.json>", os.Args[0])
	}

	registry := adapter.NewRegistry()
	if err := registry.Register(hh.Name, hh.New); err != nil {
		log.Fatal(err)
	}

	cfg, err := appconfig.Load(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	store, err := storesqlite.Open(cfg.Database.Path)
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
	}()

	for _, item := range cfg.Adapters {
		instance, err := registry.Open(item.Type, item.Settings)
		if err != nil {
			log.Fatalf("open adapter %q: %v", item.Tag, err)
		}
		for _, search := range cfg.Searches {
			if search.Adapter == item.Tag {
				if err := instance.ValidateSearch(search.Query); err != nil {
					log.Fatalf("validate search %q: %v", search.Tag, err)
				}
			}
		}
	}

	fmt.Printf("configuration loaded: %d adapters, %d profiles, %d searches; database ready\n", len(cfg.Adapters), len(cfg.Profiles), len(cfg.Searches))
}
