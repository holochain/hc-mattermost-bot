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
	prFeed := strings.TrimSpace(config.MattermostPullRequestChannelName)
	releaseFeed := strings.TrimSpace(config.MattermostReleaseCreatedChannelName)

	if teamName != "" && issueFeed != "" {
		eventHandler.OnIssuesEventOpened(
			func(ctx context.Context, deliveryID string, eventName string, event *github.IssuesEvent) error {
				issue := event.GetIssue()

				// Skip reopened issues
				if issue.GetStateReason() == "reopened" {
					return nil
				}

				return p.sendNewIssueMessage(issue, teamName, issueFeed)
			})
	} else {
		println("Mattermost team name or issue feed channel name is not set, skipping issue event listener setup")
	}

	if teamName != "" && prFeed != "" {
		eventHandler.OnPullRequestEventOpened(
			func(ctx context.Context, deliveryID string, eventName string, event *github.PullRequestEvent) error {
				repo := event.GetRepo()
				pullRequest := event.GetPullRequest()

				// Skip draft pull requests
				if pullRequest.GetDraft() {
					return nil
				}

				term := fmt.Sprintf("#%s.%s.%d", repo.GetOwner().GetName(), repo.GetName(), pullRequest.GetNumber())
				posts, err := p.findPostsByTerm(term, teamName, prFeed)
				if err != nil {
					return fmt.Errorf("failed to find posts by term %s: %w", term, err)
				}

				if len(posts) > 0 {
					// Ensure that the post is pinned
					for _, post := range posts {
						if !post.IsPinned {
							post.IsPinned = true
							err = p.client.Post.UpdatePost(post)
							if err != nil {
								return fmt.Errorf("failed to update post in channel %s: %w", prFeed, err)
							}
						}
					}

					// Pull request message already exists, do not send a duplicate
					return nil
				}

				return p.sendMessage(
					fmt.Sprintf("#%s.%s.%d %s\n\n%s", repo.GetOwner().GetName(), repo.GetName(), pullRequest.GetNumber(), pullRequest.GetTitle(), pullRequest.GetHTMLURL()),
					teamName,
					prFeed, true)
			})

		eventHandler.OnPullRequestEventReadyForReview(
			func(ctx context.Context, deliveryID string, eventName string, event *github.PullRequestEvent) error {
				repo := event.GetRepo()
				pullRequest := event.GetPullRequest()

				term := fmt.Sprintf("#%s.%s.%d", repo.GetOwner().GetName(), repo.GetName(), pullRequest.GetNumber())
				posts, err := p.findPostsByTerm(term, teamName, prFeed)
				if err != nil {
					return fmt.Errorf("failed to find posts by term %s: %w", term, err)
				}

				if len(posts) > 0 {
					// Ensure that the post is pinned
					for _, post := range posts {
						if !post.IsPinned {
							post.IsPinned = true
							err = p.client.Post.UpdatePost(post)
							if err != nil {
								return fmt.Errorf("failed to update post in channel %s: %w", prFeed, err)
							}
						}
					}

					// Pull request message already exists, do not send a duplicate
					return nil
				}

				return p.sendMessage(
					fmt.Sprintf("#%s.%s.%d %s\n\n%s", repo.GetOwner().GetName(), repo.GetName(), pullRequest.GetNumber(), pullRequest.GetTitle(), pullRequest.GetHTMLURL()),
					teamName,
					prFeed, true)
			})

		eventHandler.OnPullRequestEventClosed(
			func(ctx context.Context, deliveryID string, eventName string, event *github.PullRequestEvent) error {
				repo := event.GetRepo()
				pullRequest := event.GetPullRequest()
				term := fmt.Sprintf("#%s.%s.%d", repo.GetOwner().GetName(), repo.GetName(), pullRequest.GetNumber())

				return p.unpinMessages(term, teamName, prFeed)
			})
	} else {
		println("Mattermost team name or pull request feed channel name is not set, skipping pull request event listener setup")
	}

	if teamName != "" && releaseFeed != "" {
		eventHandler.OnReleaseEventReleased(
			func(ctx context.Context, deliveryID string, eventName string, event *github.ReleaseEvent) error {
				release := event.GetRelease()

				return p.sendMessage(
					fmt.Sprintf("%s - %s\n\n%s", release.GetName(), release.GetTagName(), release.GetHTMLURL()),
					teamName,
					releaseFeed, false)
			})

		eventHandler.OnReleaseEventPreReleased(
			func(ctx context.Context, deliveryID string, eventName string, event *github.ReleaseEvent) error {
				release := event.GetRelease()

				return p.sendMessage(
					fmt.Sprintf("Pre-release: %s - %s\n\n%s", release.GetName(), release.GetTagName(), release.GetHTMLURL()),
					teamName,
					releaseFeed, false)
			})
	} else {
		println("Mattermost team name or release feed channel name is not set, skipping release event listener setup")
	}

	p.eventHandler = eventHandler
}

func (p *Plugin) sendNewIssueMessage(issue *github.Issue, teamName, issueFeed string) error {
	return p.sendMessage(
		fmt.Sprintf("%s\n\n%s", issue.GetTitle(), issue.GetHTMLURL()),
		teamName,
		issueFeed, false)
}

func (p *Plugin) sendMessage(message, teamName, channelName string, pinned bool) error {
	botUserId := p.botUserId
	if botUserId == nil {
		return fmt.Errorf("bot user ID is nil")
	}

	team, err := p.ensureTeam(*botUserId, teamName)
	if err != nil {
		return fmt.Errorf("failed to ensure team %s: %w", teamName, err)
	}

	channel, err := p.client.Channel.GetByName(team.Id, channelName, false)
	if err != nil {
		return fmt.Errorf("failed to get channel by name %s: %w", channelName, err)
	}

	err = p.client.Post.CreatePost(&model.Post{
		IsPinned:  pinned,
		UserId:    *botUserId,
		ChannelId: channel.Id,
		Message:   message,
	})
	if err != nil {
		return fmt.Errorf("failed to create post in channel %s: %w", channelName, err)
	}

	return nil
}

func (p *Plugin) unpinMessages(term, teamName, channelName string) error {
	posts, err := p.findPostsByTerm(term, teamName, channelName)
	if err != nil {
		return fmt.Errorf("failed to find posts by term %s: %w", term, err)
	}

	for _, post := range posts {
		if post.IsPinned {
			post.IsPinned = false
			err = p.client.Post.UpdatePost(post)
			if err != nil {
				return fmt.Errorf("failed to update post in channel %s: %w", channelName, err)
			}
		}
	}

	return nil
}

func (p *Plugin) findPostsByTerm(term, teamName, channelName string) ([]*model.Post, error) {
	botUserId := p.botUserId
	if botUserId == nil {
		return nil, fmt.Errorf("bot user ID is nil")
	}

	team, err := p.ensureTeam(*botUserId, teamName)
	if err != nil {
		return nil, fmt.Errorf("failed to ensure team %s: %w", teamName, err)
	}

	channel, err := p.client.Channel.GetByName(team.Id, channelName, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get channel by name %s: %w", channelName, err)
	}

	posts, err := p.client.Post.SearchPostsInTeam(team.Id, []*model.SearchParams{{
		Terms: term,
	}})
	if err != nil {
		return nil, fmt.Errorf("failed to search posts in team %s: %w", teamName, err)
	}

	// Filter posts to only those in the specified channel
	var filteredPosts []*model.Post
	for _, post := range posts {
		if post.ChannelId == channel.Id && post.UserId == *botUserId {
			filteredPosts = append(filteredPosts, post)
		}
	}

	return filteredPosts, nil
}

func (p *Plugin) ensureTeam(botUserId, teamName string) (*model.Team, error) {
	team, err := p.client.Team.GetByName(teamName)
	if err != nil {
		return nil, fmt.Errorf("failed to get team by name %s: %w", teamName, err)
	}

	foundBotUserInTeam := false
	page := 0
out:
	for {
		memberList, err := p.client.Team.ListMembers(team.Id, page, 100)
		if err != nil {
			return nil, fmt.Errorf("failed to list team members: %w", err)
		}

		if len(memberList) < 100 {
			break
		}

		for _, member := range memberList {
			if member.UserId == botUserId {
				foundBotUserInTeam = true
				break out
			}
		}

		page += 1
	}

	if !foundBotUserInTeam {
		_, err = p.client.Team.CreateMember(team.Id, botUserId)
	}

	return team, nil
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
