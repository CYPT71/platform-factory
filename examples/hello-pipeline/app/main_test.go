package main

import "testing"

func TestGreetingDefaultsToWorld(t *testing.T) {
	if got := greeting(""); got != "hello, world, from the platform-factory pipeline system" {
		t.Fatalf("greeting=%q", got)
	}
}

func TestGreetingUsesName(t *testing.T) {
	if got := greeting("pipeline"); got != "hello, pipeline, from the platform-factory pipeline system" {
		t.Fatalf("greeting=%q", got)
	}
}
