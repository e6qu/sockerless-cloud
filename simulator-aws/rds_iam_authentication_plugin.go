package main

import (
	"embed"
	"encoding/base64"
	"fmt"

	"github.com/e6qu/sockerless-cloud/sim/dbengine"
)

// rdsIAMAuthenticationPlugin is the server authentication plugin RDS for
// MySQL, RDS for MariaDB and Aurora MySQL identify IAM database users with:
// CREATE USER ... IDENTIFIED WITH AWSAuthenticationPlugin AS 'RDS'.
const rdsIAMAuthenticationPlugin = "AWSAuthenticationPlugin"

// rds_auth_plugin/build.sh builds the plugin for each engine and architecture.
//
//go:embed rds_auth_plugin/*.so
var rdsAuthPlugins embed.FS

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

// rdsInstallPluginScript writes the plugin for the engine's architecture into
// its plugin directory, replacing the file by rename so a loaded copy stays
// mapped, and installs the plugin unless the engine has it loaded.
const rdsInstallPluginScript = `set -e
case "$(uname -m)" in
	x86_64) encoded="$AMD64" ;;
	aarch64) encoded="$ARM64" ;;
	*) echo "no AWSAuthenticationPlugin build for $(uname -m)"; exit 1 ;;
esac
mkdir -p "$PLUGIN_DIR"
plugin="$PLUGIN_DIR/aws_authentication_plugin.so"
if [ ! -f "$plugin" ] || [ "$(base64 -w 0 "$plugin")" != "$encoded" ]; then
	printf %s "$encoded" | base64 -d > "$plugin.new"
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
	encoded := map[string]string{}
	for _, arch := range []string{"amd64", "arm64"} {
		plugin, err := rdsAuthPlugins.ReadFile("rds_auth_plugin/" + flavor + "-" + arch + ".so")
		if err != nil {
			return fmt.Errorf("read the %s AWSAuthenticationPlugin build: %w", arch, err)
		}
		encoded[arch] = base64.StdEncoding.EncodeToString(plugin)
	}
	err := engine.Exec([]string{"env",
		"AMD64=" + encoded["amd64"], "ARM64=" + encoded["arm64"],
		"PLUGIN_DIR=" + engine.Engine.DataPath + "/" + rdsPluginDirName,
		"CLIENT=" + engine.Engine.Client, "ROOT_PASSWORD=" + rootPassword,
		"sh", "-c", rdsInstallPluginScript})
	if err != nil {
		return fmt.Errorf("install %s: %w", rdsIAMAuthenticationPlugin, err)
	}
	return nil
}
