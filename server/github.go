package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cbrgm/githubevents/v2/githubevents"
	"github.com/google/go-github/v81/github"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
)

func (p *Plugin) startGithubEventListener() {
	config := p.configuration.Clone()

	eventHandler := githubevents.New(config.WebhookSecretToken)

	teamName := strings.TrimSpace(config.MattermostTeamName)
	issueFeed := strings.TrimSpace(config.MattermostIssueFeedChannelName)
	prFeed := strings.TrimSpace(config.MattermostPullRequestChannelName)
	releaseFeed := strings.TrimSpace(config.MattermostReleaseCreatedChannelName)

	// Log all received events, to debug what information is actually coming from GitHub
	eventHandler.OnBeforeAny(func(ctx context.Context, deliveryID string, eventName string, event interface{}) error {
		p.API.LogInfo("Received GitHub event", "event_name", eventName, "delivery_id", deliveryID)
		return nil
	})

	if teamName != "" && issueFeed != "" {
		eventHandler.OnIssuesEventOpened(
			func(ctx context.Context, deliveryID string, eventName string, event *github.IssuesEvent) error {
				repo := event.GetRepo()
				issue := event.GetIssue()

				if err := p.validateRepoProperties(repo, event); err != nil {
					return err
				}

				tag := fmt.Sprintf("#%s.%s.%d", repo.GetOwner().GetLogin(), repo.GetName(), issue.GetNumber())
				posts, err := p.findPostsByTag(tag, teamName, issueFeed)
				if err != nil {
					return err
				}
				if len(posts) > 0 {
					// Skip creating duplicate posts for this issue
					return nil
				}

				return p.sendMessage(
					fmt.Sprintf("%s\n%s\n%s", issue.GetTitle(), issue.GetHTMLURL(), tag),
					teamName,
					issueFeed, false)
			})
	} else {
		println("Mattermost team name or issue feed channel name is not set, skipping issue event listener setup")
	}

	if teamName != "" && prFeed != "" {
		pullRequestUpdatedHook := func(ctx context.Context, deliveryID string, eventName string, event *github.PullRequestEvent) error {
			return p.handlePullRequestUpdated(teamName, prFeed, event)
		}

		eventHandler.OnPullRequestEventOpened(pullRequestUpdatedHook)
		eventHandler.OnPullRequestEventReopened(pullRequestUpdatedHook)
		eventHandler.OnPullRequestEventReadyForReview(pullRequestUpdatedHook)
		eventHandler.OnPullRequestEventReviewRequested(pullRequestUpdatedHook)

		eventHandler.OnPullRequestEventClosed(
			func(ctx context.Context, deliveryID string, eventName string, event *github.PullRequestEvent) error {
				repo := event.GetRepo()
				pullRequest := event.GetPullRequest()

				if err := p.validateRepoProperties(repo, event); err != nil {
					return err
				}

				term := fmt.Sprintf("#%s.%s.%d", repo.GetOwner().GetLogin(), repo.GetName(), pullRequest.GetNumber())

				return p.unpinMessages(term, teamName, prFeed)
			})
	} else {
		println("Mattermost team name or pull request feed channel name is not set, skipping pull request event listener setup")
	}

	if teamName != "" && releaseFeed != "" {
		eventHandler.OnReleaseEventReleased(
			func(ctx context.Context, deliveryID string, eventName string, event *github.ReleaseEvent) error {
				repo := event.GetRepo()
				release := event.GetRelease()

				if err := p.validateRepoProperties(repo, event); err != nil {
					return err
				}

				tag := fmt.Sprintf("#%s.%s.%s", repo.GetOwner().GetLogin(), repo.GetName(), release.GetTagName())
				posts, err := p.findPostsByTag(tag, teamName, releaseFeed)
				if err != nil {
					return fmt.Errorf("failed to find posts by tag %s: %w", releaseFeed, err)
				}
				if len(posts) > 0 {
					// Skip creating duplicate events
					return nil
				}

				return p.sendMessage(
					fmt.Sprintf("%s\n%s", releaseTable(repo, release, false), tag),
					teamName,
					releaseFeed, false)
			})

		eventHandler.OnReleaseEventPreReleased(
			func(ctx context.Context, deliveryID string, eventName string, event *github.ReleaseEvent) error {
				repo := event.GetRepo()
				release := event.GetRelease()

				if err := p.validateRepoProperties(repo, event); err != nil {
					return err
				}

				tag := fmt.Sprintf("#%s.%s.%s", repo.GetOwner().GetLogin(), repo.GetName(), release.GetTagName())
				posts, err := p.findPostsByTag(tag, teamName, releaseFeed)
				if err != nil {
					return fmt.Errorf("failed to find posts by tag %s: %w", releaseFeed, err)
				}
				if len(posts) > 0 {
					// Skip creating duplicate events
					return nil
				}

				return p.sendMessage(
					fmt.Sprintf("%s\n%s", releaseTable(repo, release, true), tag),
					teamName,
					releaseFeed, false)
			})
	} else {
		println("Mattermost team name or release feed channel name is not set, skipping release event listener setup")
	}

	p.eventHandler = eventHandler
}

// handlePullRequestUpdated decides whether a pull request should be posted (or re-pinned) to
// Mattermost. GitHub can deliver multiple webhook events for the same pull request in quick
// succession (e.g. "opened" and "review_requested" when a non-draft PR is created with reviewers
// already assigned), and each delivery is handled on its own goroutine with no coordination
// between them. To avoid posting the same pull request twice, creating the post is gated behind
// an atomic claim so only one concurrent delivery actually creates it.
func (p *Plugin) handlePullRequestUpdated(teamName, prFeed string, event *github.PullRequestEvent) error {
	repo := event.GetRepo()
	pullRequest := event.GetPullRequest()

	// Skip draft pull requests
	if pullRequest.GetDraft() {
		p.API.LogInfo("Pull request is a draft, skipping notification", "repo", repo.GetFullName(), "pr_number", pullRequest.GetNumber())
		return nil
	}

	// Skip pull requests that don't have any requested reviewers
	if (pullRequest.RequestedTeams == nil || len(pullRequest.RequestedTeams) == 0) && (pullRequest.RequestedReviewers == nil || len(pullRequest.RequestedReviewers) == 0) {
		p.API.LogInfo("Pull request has no requested reviewers or teams, skipping notification", "repo", repo.GetFullName(), "pr_number", pullRequest.GetNumber())
		return nil
	}

	if err := p.validateRepoProperties(repo, event); err != nil {
		return err
	}

	tag := fmt.Sprintf("#%s.%s.%d", repo.GetOwner().GetLogin(), repo.GetName(), pullRequest.GetNumber())
	posts, err := p.findPostsByTag(tag, teamName, prFeed)
	if err != nil {
		return fmt.Errorf("failed to find posts by tag %s: %w", tag, err)
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

	// Multiple webhook deliveries for this pull request can reach this point having each seen no
	// existing post. Only the delivery that wins this atomic claim is allowed to create it.
	claimed, err := p.tryClaimPost(tag)
	if err != nil {
		return fmt.Errorf("failed to claim post for tag %s: %w", tag, err)
	}
	if !claimed {
		p.API.LogInfo("Pull request post already claimed by another event, skipping duplicate", "tag", tag)
		return nil
	}

	if err := p.sendMessage(
		fmt.Sprintf("%s\n%s\n%s", pullRequest.GetTitle(), pullRequest.GetHTMLURL(), tag),
		teamName,
		prFeed, true); err != nil {
		// Posting failed, so release the claim to allow a future event to retry.
		p.releasePostClaim(tag)
		return err
	}

	return nil
}

// postClaimTTL bounds how long a post claim occupies the KV store. It only needs to outlive the
// window where a concurrent or slightly delayed duplicate delivery could still be racing against
// the original one (near-simultaneous webhook deliveries, Mattermost search-index lag on the
// findPostsByTag check). Once that window has passed, findPostsByTag itself is the authoritative
// record that the pull request was already posted, so the claim no longer needs to exist.
const postClaimTTL = time.Hour

// tryClaimPost atomically claims the right to create the Mattermost post identified by tag.
// It returns true only for the single caller that wins the race for a given tag; all other
// concurrent or subsequent callers receive false and must not post again. The claim expires
// after postClaimTTL so successful claims don't accumulate in the KV store indefinitely.
func (p *Plugin) tryClaimPost(tag string) (bool, error) {
	claimed, err := p.client.KV.Set(postClaimKey(tag), true, pluginapi.SetAtomic(nil), pluginapi.SetExpiry(postClaimTTL))
	if err != nil {
		return false, err
	}
	return claimed, nil
}

// releasePostClaim releases a claim taken by tryClaimPost, allowing a future event for the same
// tag to retry after a failed post.
func (p *Plugin) releasePostClaim(tag string) {
	if err := p.client.KV.Delete(postClaimKey(tag)); err != nil {
		p.API.LogWarn("failed to release post claim", "tag", tag, "err", err)
	}
}

func postClaimKey(tag string) string {
	return "post-claim:" + tag
}

func releaseTable(repo *github.Repository, release *github.RepositoryRelease, isPreRelease bool) string {
	var preReleaseRow string
	if isPreRelease {
		preReleaseRow = "| Pre-release | Yes |"
	} else {
		preReleaseRow = "| Pre-release | No |"
	}

	return fmt.Sprintf(`
| Key        | Value |
| ---------- | ----- |
| Repository | %s    |
| Name       | %s    |
| Tag        | %s    |
| URL        | %s    |
%s
`, repo.GetName(), release.GetName(), release.GetTagName(), release.GetHTMLURL(), preReleaseRow)
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
	posts, err := p.findPostsByTag(term, teamName, channelName)
	if err != nil {
		return fmt.Errorf("failed to find posts by term %s: %w", term, err)
	}

	if posts == nil || len(posts) == 0 {
		// No matching post to unpin, return early
		p.API.LogInfo("No posts found with term, skipping unpinning", "term", term, "channel", channelName)
		return nil
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

func (p *Plugin) findPostsByTag(term, teamName, channelName string) ([]*model.Post, error) {
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

	// Search only within the specific channel to avoid timeout
	posts, err := p.client.Post.SearchPostsInTeam(team.Id, []*model.SearchParams{{
		Terms:                  term,
		IsHashtag:              true,
		IncludeDeletedChannels: false,
		InChannels:             []string{channelName},
		FromUsers:              []string{"holochain-bot"},
	}})
	if err != nil {
		return nil, fmt.Errorf("failed to search posts in team %s: %w", teamName, err)
	}

	// Filter to ensure only posts from the bot in the specified channel
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

		for _, member := range memberList {
			if member.UserId == botUserId {
				foundBotUserInTeam = true
				break out
			}
		}

		if len(memberList) < 100 {
			break
		}

		page += 1
	}

	if !foundBotUserInTeam {
		_, err = p.client.Team.CreateMember(team.Id, botUserId)
		if err != nil {
			return nil, fmt.Errorf("failed to add bot to team %s: %w", teamName, err)
		}
	}

	return team, nil
}

// ServerHTTP handles HTTP requests made to the plugin.
func (p *Plugin) ServeHTTP(_ *plugin.Context, _ http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/github" {
		p.API.LogInfo("GitHub event listener called")
		if p.eventHandler != nil {
			p.API.LogInfo("Handling GitHub event request")
			if err := p.eventHandler.HandleEventRequest(r); err != nil {
				p.API.LogWarn("error handling github event request: %v\n", err)
			}
		} else {
			p.API.LogWarn("event handler is nil")
		}
	} else {
		p.API.LogInfo("unknown path: %s\n", r.URL.Path)
	}
}

func (p *Plugin) validateRepoProperties(repo *github.Repository, event interface{}) error {
	if repo.GetOwner() == nil || strings.TrimSpace(repo.GetOwner().GetLogin()) == "" {
		payloadJson, err := json.Marshal(event)
		if err != nil {
			return err
		}

		p.API.LogInfo("Repository owner login is empty", "payload", string(payloadJson))
		return errors.New("repository owner login is empty")
	}

	if strings.TrimSpace(repo.GetName()) == "" {
		payloadJson, err := json.Marshal(event)
		if err != nil {
			return err
		}

		p.API.LogInfo("Repository name is empty", "payload", string(payloadJson))
		return errors.New("repository name is empty")
	}

	return nil
}
