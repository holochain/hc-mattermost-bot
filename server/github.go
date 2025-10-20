package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cbrgm/githubevents/v2/githubevents"
	"github.com/google/go-github/v76/github"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
)

func (p *Plugin) startGithubEventListener() {
	config := p.configuration.Clone()

	eventHandler := githubevents.New(config.WebhookSecretToken)

	teamName := strings.TrimSpace(config.MattermostTeamName)
	issueFeed := strings.TrimSpace(config.MattermostIssueFeedChannelName)

	if teamName != "" && issueFeed != "" {
		eventHandler.OnIssuesEventOpened(
			func(ctx context.Context, deliveryID string, eventName string, event *github.IssuesEvent) error {
				// Skip draft issues
				if event.GetIssue().GetDraft() {
					return nil
				}

				// Skip reopened issues
				if event.GetIssue().GetStateReason() == "reopened" {
					return nil
				}

				return p.sendNewIssueMessage(event.GetIssue(), teamName, issueFeed)
			})
	} else {
		println("Mattermost team name or issue feed channel name is not set, skipping issue event listener setup")
	}

	p.eventHandler = eventHandler
}

func (p *Plugin) sendNewIssueMessage(issue *github.Issue, teamName, issueFeed string) error {
	return p.sendMessage(
		fmt.Sprintf("%s\n\n%s", issue.GetTitle(), issue.GetHTMLURL()),
		teamName,
		issueFeed)
}

func (p *Plugin) sendMessage(message, teamName, channelName string) error {
	botUserId := p.botUserId
	if botUserId == nil {
		return fmt.Errorf("bot user ID is nil")
	}

	team, err := p.client.Team.GetByName(teamName)
	if err != nil {
		return fmt.Errorf("failed to get team by name %s: %w", teamName, err)
	}

	foundBotUserInTeam := false
	page := 0
out:
	for {
		memberList, err := p.client.Team.ListMembers(team.Id, page, 100)
		if err != nil {
			return fmt.Errorf("failed to list team members: %w", err)
		}

		if len(memberList) < 100 {
			break
		}

		for _, member := range memberList {
			if member.UserId == *botUserId {
				foundBotUserInTeam = true
				break out
			}
		}

		page += 1
	}

	if !foundBotUserInTeam {
		_, err = p.client.Team.CreateMember(team.Id, *botUserId)
	}

	channel, err := p.client.Channel.GetByName(team.Id, channelName, false)
	if err != nil {
		return fmt.Errorf("failed to get channel by name %s: %w", channelName, err)
	}

	err = p.client.Post.CreatePost(&model.Post{
		IsPinned:  true,
		UserId:    *botUserId,
		ChannelId: channel.Id,
		Message:   message,
	})
	if err != nil {
		return fmt.Errorf("failed to create post in channel %s: %w", channelName, err)
	}

	return nil
}

// ServerHTTP handles HTTP requests made to the plugin.
func (p *Plugin) ServeHTTP(_ *plugin.Context, _ http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/github" {
		println("GitHub event listener called")
		if p.eventHandler != nil {
			println("Handling GitHub event request")
			if err := p.eventHandler.HandleEventRequest(r); err != nil {
				fmt.Printf("error handling github event request: %v\n", err)
			}
		} else {
			fmt.Println("event handler is nil")
		}
	} else {
		fmt.Printf("unknown path: %s\n", r.URL.Path)
	}
}
