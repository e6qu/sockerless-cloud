package main

import (
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// rdsIAMAuthenticationPlugin is the server authentication plugin RDS for
// MySQL, RDS for MariaDB and Aurora MySQL identify IAM database users with:
// CREATE USER ... IDENTIFIED WITH AWSAuthenticationPlugin AS 'RDS'.
const rdsIAMAuthenticationPlugin = "AWSAuthenticationPlugin"

// rdsAuthPluginSource is AWSAuthenticationPlugin's source, which the engine
// compiles for its own architecture with no C library.
//
//go:embed rds_auth_plugin/aws_authentication_plugin.c
var rdsAuthPluginSource []byte

const rdsAuthPluginBuildImage = "mirror.gcr.io/library/alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a"

// rdsAuthPluginDockerfile compiles the plugin for one engine family; define
// selects MariaDB's plugin interface.
func rdsAuthPluginDockerfile(define string) string {
	return `FROM ` + rdsAuthPluginBuildImage + ` AS build
COPY ` + sim.EngineBuildTrustFile + ` /tmp/
RUN cat /tmp/` + sim.EngineBuildTrustFile + ` >> /etc/ssl/certs/ca-certificates.crt && apk add --no-cache clang22 lld22
COPY aws_authentication_plugin.c /src/
RUN mkdir /out
RUN clang ` + define + ` -std=c11 -O2 -Wall -Wextra -Werror \
	-fPIC -ffreestanding -fno-builtin -fno-stack-protector -fvisibility=hidden -nostdlib -shared \
	-fuse-ld=lld -Wl,--build-id=none -Wl,-z,relro,-z,now -Wl,-s \
	-o /out/aws_authentication_plugin.so /src/aws_authentication_plugin.c

FROM scratch
COPY --from=build /out/aws_authentication_plugin.so /aws_authentication_plugin.so
`
}

var (
	rdsAuthPluginBuildsMu sync.Mutex
	rdsAuthPluginBuilds   = map[string][]byte{}
)

// rdsAuthPlugin returns the plugin for flavor ("mysql" or "mariadb") built on
// the engine for the engine's architecture.
func rdsAuthPlugin(ctx context.Context, flavor string) ([]byte, error) {
	define := ""
	if flavor == "mariadb" {
		define = "-DMARIADB"
	}
	cli := sim.DockerClient()
	if cli == nil {
		return nil, errors.New("building AWSAuthenticationPlugin needs the container engine")
	}
	image, _, err := sim.EnsureEngineBuild(ctx, cli, sim.EngineBuild{
		Name:  "sockerless-rds-auth-plugin-" + flavor,
		Bases: []string{rdsAuthPluginBuildImage},
		Context: func(string) (map[string][]byte, error) {
			return map[string][]byte{
				"Dockerfile":                  []byte(rdsAuthPluginDockerfile(define)),
				"aws_authentication_plugin.c": rdsAuthPluginSource,
			}, nil
		},
	})
	if err != nil {
		return nil, err
	}
	rdsAuthPluginBuildsMu.Lock()
	defer rdsAuthPluginBuildsMu.Unlock()
	if plugin, ok := rdsAuthPluginBuilds[image]; ok {
		return plugin, nil
	}
	plugin, err := sim.ReadEngineImageFile(ctx, cli, image, "/aws_authentication_plugin.so")
	if err != nil {
		return nil, err
	}
	rdsAuthPluginBuilds[image] = plugin
	return plugin, nil
}

// The engine loads plugins from a directory in its data volume, so a snapshot
// or restore of the volume keeps the plugin with the users identified with it.
// MariaDB lists every directory of its data directory as a database unless
// told to ignore it.
const rdsPluginDirName = ".sockerless-plugin"

func rdsPluginArgs(engine dbengine.Engine) []string {
	args := []string{"--plugin-dir=" + engine.DataPath + "/" + rdsPluginDirName}
	if engine.Client == dbengine.MariaDB114.Client {
		args = append(args, "--ignore-db-dirs="+rdsPluginDirName)
	}
	return args
}

// rdsInstallPluginScript writes the plugin into the engine's plugin directory,
// replacing the file by rename so a loaded copy stays mapped, and installs the
// plugin unless the engine has it loaded.
const rdsInstallPluginScript = `set -e
mkdir -p "$PLUGIN_DIR"
plugin="$PLUGIN_DIR/aws_authentication_plugin.so"
if [ ! -f "$plugin" ] || [ "$(base64 -w 0 "$plugin")" != "$PLUGIN" ]; then
	printf %s "$PLUGIN" | base64 -d > "$plugin.new"
	chmod 644 "$plugin.new"
	mv "$plugin.new" "$plugin"
fi
loaded=$("$CLIENT" --user=root --password="$ROOT_PASSWORD" --batch --skip-column-names \
	--execute="SELECT COUNT(*) FROM information_schema.plugins WHERE plugin_name = 'AWSAuthenticationPlugin'")
if [ "$loaded" = 0 ]; then
	"$CLIENT" --user=root --password="$ROOT_PASSWORD" \
		--execute="INSTALL PLUGIN AWSAuthenticationPlugin SONAME 'aws_authentication_plugin.so'"
fi
`

// rdsInstallIAMAuthenticationPlugin gives a MySQL-family engine
// AWSAuthenticationPlugin.
func rdsInstallIAMAuthenticationPlugin(engine *dbengine.Instance, rootPassword string) error {
	flavor := "mysql"
	if engine.Engine.Client == dbengine.MariaDB114.Client {
		flavor = "mariadb"
	}
	plugin, err := rdsAuthPlugin(context.Background(), flavor)
	if err != nil {
		return fmt.Errorf("build %s: %w", rdsIAMAuthenticationPlugin, err)
	}
	err = engine.Exec([]string{"env",
		"PLUGIN=" + base64.StdEncoding.EncodeToString(plugin),
		"PLUGIN_DIR=" + engine.Engine.DataPath + "/" + rdsPluginDirName,
		"CLIENT=" + engine.Engine.Client, "ROOT_PASSWORD=" + rootPassword,
		"sh", "-c", rdsInstallPluginScript})
	if err != nil {
		return fmt.Errorf("install %s: %w", rdsIAMAuthenticationPlugin, err)
	}
	return nil
}
