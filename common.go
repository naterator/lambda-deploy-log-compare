package main

const appName = "lambda-deploy-log-compare"

// appVersion is set at build time via ldflags:
//
//	go build -ldflags "-X main.appVersion=v1.2.3"
var appVersion = "dev"
