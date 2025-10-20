#!/usr/bin/env bash

set -e

curl -X POST -H "content-type: application/json" -H "X-GitHub-Event: issues" -d @sample/issue.json \
  http://localhost:8065/plugins/org.holochain.mm-plugin/github
