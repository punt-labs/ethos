package main

import "github.com/punt-labs/ethos/internal/attribute"

func runPersonality(args []string) {
	runAttributeSubcmd(attribute.Personalities, args)
}
