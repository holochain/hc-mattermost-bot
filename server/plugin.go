package main

import (
	"sync"

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

	return nil
}

// OnDeactivate is invoked when the plugin is deactivated.
func (p *Plugin) OnDeactivate() error {
	return nil
}

// See https://developers.mattermost.com/extend/plugins/server/reference/
