# MatterMost plugin

## Set up a local MatterMost instance

You need a Mattermost instance to test with, either locally or remotely hosted. You can follow these [instructions](https://docs.mattermost.com/deployment-guide/server/containers/install-docker.html)
if you want to set up a local instance using Docker.

Choose the option without nginx, as you don't need TLS locally. That means starting with the command:

```shell
docker compose -f docker-compose.yml -f docker-compose.without-nginx.yml up -d
```

Update the configuration to allow uploading plugins:

```shell
docker compose cp mattermost:/mattermost/config/config.json .
```

Set the fields `PluginSettings.EnableUploads` to `true`, and `ServiceSettings.EnableLocalMode` to `true`.

Then copy the file back to the container:

```shell
docker compose cp config.json mattermost:/mattermost/config/config.json
```

Restart the container:

```shell
docker compose restart mattermost
```

Finally, create an admin user so you can get started.

```shell
 docker compose exec mattermost mmctl --local user create --email test@local.net --username test --password testtest --system-admin
```
