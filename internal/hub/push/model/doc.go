// Package model holds the types shared by the push packages (rules, engine, action,
// webpush, ntfy), the store functions for alerts, rules and targets, and the API handlers.
//
// It is a leaf: it imports nothing from hub/, so hub/store and hub/api can use it without a
// cycle (the dependency rule is hub/push -> hub/store, hub/events, hub/push/model; never
// hub/api). There is no logic here beyond Settings.Defaults and JSON round trips.
// Field names and JSON tags follow docs/spec/push.md and the schemas of docs/spec/api.yaml.
package model
