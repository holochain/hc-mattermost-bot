package main

import (
	"encoding/json"
	"sort"
	"sync"

	"github.com/agnivade/levenshtein"
	"github.com/cbrgm/githubevents/v2/githubevents"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"
)

// Plugin implements the interface expected by the Mattermost server to communicate between the server and plugin processes.
type Plugin struct {
	plugin.MattermostPlugin

	// client is the Mattermost server API client.
	client *pluginapi.Client

	// configurationLock synchronizes access to the configuration.
	configurationLock sync.RWMutex

	// configuration is the active plugin configuration. Consult getConfiguration and
	// setConfiguration for usage.
	configuration *Configuration

	botUserId *string

	eventHandler *githubevents.EventHandler
}

// OnActivate is invoked when the plugin is activated. If an error is returned, the plugin will be deactivated.
func (p *Plugin) OnActivate() error {
	p.client = pluginapi.NewClient(p.API, p.Driver)

	// Ensure the bot user is created, or get the ID of the existing bot user.
	botUserId, err := p.client.Bot.EnsureBot(&model.Bot{
		Username:    "holochain-bot",
		DisplayName: "Holochain Bot",
		Description: "A bot account created by the Holochain Mattermost plugin.",
	})
	if err != nil {
		return errors.Wrap(err, "failed to ensure bot account")
	}

	// Store the bot user ID for later use.
	p.botUserId = &botUserId

	p.selfCheck()

	return nil
}

// OnDeactivate is invoked when the plugin is deactivated.
func (p *Plugin) OnDeactivate() error {
	return nil
}

func (p *Plugin) selfCheck() {
	teamName := p.configuration.MattermostTeamName
	if teamName != "" {
		team, err := p.client.Team.GetByName(teamName)
		if err != nil {
			teams, err := p.client.Team.List()
			if err != nil {
				p.API.LogWarn("self check failed: unable to list teams", "err", err)
				return
			}

			teamNames := make([]string, len(teams))
			for i, team := range teams {
				teamNames[i] = team.Name
			}

			jsonTeamNames, err := json.Marshal(teamNames)
			if err != nil {
				p.API.LogWarn("self check failed: unable to marshal teams", "err", err)
				return
			}

			p.API.LogInfo("Available teams", "names", string(jsonTeamNames))

			p.API.LogWarn("self check failed: unable to find configured team", "teamName", teamName, "err", err)
			return
		}

		page := 0
		numPerPage := 100
		channelNames := make([]string, 0)
		for {
			channels, err := p.client.Channel.ListPublicChannelsForTeam(team.Id, page, numPerPage)
			if err != nil {
				p.API.LogWarn("self check failed: unable to list channels", "err", err)
				return
			}

			for _, channel := range channels {
				println("channel:", channel.Name)
				if channel.Name != "" {
					channelNames = append(channelNames, channel.Name)
				} else {
					p.API.LogWarn("self check warn: found channel with empty name", "channelDisplayName", channel.DisplayName)
				}
			}

			if len(channels) < numPerPage {
				break
			}
			page += 1
		}

		channelsOk := true

		foundIssuesChannel := false
		issueFeedChannelName := p.configuration.MattermostIssueFeedChannelName
		if issueFeedChannelName != "" {
			for _, name := range channelNames {
				if name == issueFeedChannelName {
					foundIssuesChannel = true
					break
				}
			}
		}

		foundPRChannel := false
		prChannelName := p.configuration.MattermostPullRequestChannelName
		if prChannelName != "" {
			for _, name := range channelNames {
				if name == prChannelName {
					foundPRChannel = true
					break
				}
			}
		}

		foundReleaseChannel := false
		releaseChannelName := p.configuration.MattermostReleaseCreatedChannelName
		if releaseChannelName != "" {
			for _, name := range channelNames {
				if name == releaseChannelName {
					foundReleaseChannel = true
					break
				}
			}
		}

		if (issueFeedChannelName != "" && !foundIssuesChannel) ||
			(prChannelName != "" && !foundPRChannel) ||
			(releaseChannelName != "" && !foundReleaseChannel) {
			channelsOk = false

			p.API.LogWarn("self check: one or more configured channels were not found in the team", "teamName", teamName)

			jsonChannelNames, err := json.Marshal(channelNames)
			if err != nil {
				p.API.LogWarn("self check failed: unable to marshal channel names", "err", err)
				return
			}

			p.API.LogInfo("self check: existing channels", "names", string(jsonChannelNames))
		}

		if issueFeedChannelName != "" && !foundIssuesChannel {
			suggestedNames, err := suggestChannelNames(issueFeedChannelName, channelNames)
			if err != nil {
				p.API.LogWarn("self check failed: unable to recommend channel names", "err", err)
				return
			}

			p.API.LogWarn("self check error: unable to find configured channel, did you mean one of these?", "channelName", issueFeedChannelName, "suggestedNames", suggestedNames)
		}

		if prChannelName != "" && !foundPRChannel {
			suggestedNames, err := suggestChannelNames(prChannelName, channelNames)
			if err != nil {
				p.API.LogWarn("self check failed: unable to recommend channel names", "err", err)
			}

			p.API.LogWarn("self check error: unable to find configured pull request channel", "channelName", prChannelName, "suggestedNames", suggestedNames)
		}

		if releaseChannelName != "" && !foundReleaseChannel {
			suggestedNames, err := suggestChannelNames(releaseChannelName, channelNames)
			if err != nil {
				p.API.LogWarn("self check failed: unable to recommend channel names", "err", err)
			}

			p.API.LogWarn("self check error: unable to find configured release created channel", "channelName", releaseChannelName, "suggestedNames", suggestedNames)
		}

		if channelsOk {
			p.API.LogInfo("self check passed: configured team and channels found", "teamName", teamName)
		}
	} else {
		p.API.LogWarn("self check skipped: no team configured")
	}
}

func suggestChannelNames(configuredChannelName string, allChannelNames []string) (string, error) {
	distances := make([]struct {
		Name string
		Dist int
	}, 0)

	// Compute Levenshtein distance to all existing channel names
	for _, name := range allChannelNames {
		distances = append(distances, struct {
			Name string
			Dist int
		}{
			Name: name,
			Dist: levenshtein.ComputeDistance(configuredChannelName, name),
		})
	}

	// Sort by distance
	sort.SliceStable(distances, func(i, j int) bool {
		return distances[i].Dist < distances[j].Dist
	})

	// Take up to 10 closest names
	take := min(10, len(allChannelNames))

	// Get the name field from the closest names
	closestNames := make([]string, take)
	for i := range take {
		closestNames[i] = distances[i].Name
	}

	suggestedNames, err := json.Marshal(closestNames)
	if err != nil {
		return "", err
	}

	return string(suggestedNames), nil
}

// See https://developers.mattermost.com/extend/plugins/server/reference/
