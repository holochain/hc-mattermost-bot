#!/usr/bin/env bash

set -e

curl -H "content-type: application/json" -H "X-GitHub-Event: issues" -d @sample/issue.json \
  http://localhost:8065/plugins/org.holochain.mm-plugin/github

curl -H "content-type: application/json" -H "X-GitHub-Event: pull_request" -d @sample/pull_request_1.json \
  http://localhost:8065/plugins/org.holochain.mm-plugin/github

curl -H "content-type: application/json" -H "X-GitHub-Event: pull_request" -d @sample/pull_request_2.json \
  http://localhost:8065/plugins/org.holochain.mm-plugin/github

curl -H "content-type: application/json" -H "X-GitHub-Event: pull_request" -d @sample/pull_request_closed_2.json \
  http://localhost:8065/plugins/org.holochain.mm-plugin/github

curl -H "content-type: application/json" -H "X-GitHub-Event: release" -d @sample/prerelease.json \
  http://localhost:8065/plugins/org.holochain.mm-plugin/github

curl -H "content-type: application/json" -H "X-GitHub-Event: release" -d @sample/release.json \
  http://localhost:8065/plugins/org.holochain.mm-plugin/github
