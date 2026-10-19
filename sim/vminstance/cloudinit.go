package vminstance

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"path"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"go.yaml.in/yaml/v3"
)

// CloudInit is the image's cloud-init as the cloud's datasource feeds it.
// The guest decides at boot whether its distribution carries cloud-init and
// which build and default user it has; the cloud says the rest.
type CloudInit struct {
	// Platform names the cloud's images, which differ in their default user:
	// "ec2", "gce" or "azure".
	Platform string
	// Datasource is the datasource cloud-init reports, as
	// "DataSourceEc2Local".
	Datasource string
	// InstanceID is the identity cloud-init keys its per-instance state on.
	InstanceID string
	// DefaultUser names the default user when the datasource does, as
	// Azure's adminUsername; empty takes the distribution's.
	DefaultUser string
	// DefaultUserKeys are the public keys the datasource gives the default
	// user: an EC2 key pair, an Azure osProfile's keys.
	DefaultUserKeys []string
}

// CloudInitScriptPath is where the instance's cloud-init stages live.
const CloudInitScriptPath = "/usr/lib/sockerless/cloud-init.sh"

//go:embed cloudinit.sh
var cloudInitScript string

const cloudInitEnvPath = "/var/lib/cloud/sockerless/env.sh"

// cloudInitSeed renders the user data into the files cloud-init's modules
// work from.
func cloudInitSeed(ci CloudInit, userData []byte) ([]sim.SeedFile, error) {
	if ci.InstanceID == "" || strings.ContainsAny(ci.InstanceID, "/\x00") {
		return nil, fmt.Errorf("cloud-init needs an instance id without slashes, got %q", ci.InstanceID)
	}
	dir := "/var/lib/cloud/instances/" + ci.InstanceID
	r := &cloudInitRender{dir: dir}
	parts, warnings := cloudInitParts(userData)
	r.warnings = warnings
	var config *yaml.Node
	for i, p := range parts {
		name := fmt.Sprintf("part-%03d", i+1)
		switch p.kind {
		case partScript:
			r.file("scripts/"+name, 0o700, p.body)
		case partCloudConfig:
			var doc yaml.Node
			if err := yaml.Unmarshal(p.body, &doc); err != nil {
				r.warn("Failed loading yaml blob. " + err.Error())
				continue
			}
			if len(doc.Content) == 0 {
				continue
			}
			if config == nil {
				config = doc.Content[0]
			} else {
				mergeCloudConfig(config, doc.Content[0])
			}
		}
	}
	var cfg cloudConfig
	if config != nil {
		r.decode(config, &cfg)
	}
	r.render(cfg, ci)
	r.file("user-data.txt", 0o600, userData)
	if len(r.warnings) > 0 {
		r.file("warnings", 0o600, []byte(strings.Join(r.warnings, "\n")+"\n"))
	}
	env := strings.Join([]string{
		"CI_INSTANCE_ID=" + shellQuote(ci.InstanceID),
		"CI_PLATFORM=" + shellQuote(ci.Platform),
		"CI_DATASOURCE=" + shellQuote(ci.Datasource),
		"CI_DEFAULT_USER=" + shellQuote(ci.DefaultUser),
		"CI_FINAL_MESSAGE=" + shellQuote(cfg.FinalMessage),
		"",
	}, "\n")
	seed := []sim.SeedFile{
		{Path: CloudInitScriptPath, Mode: 0o755, Data: []byte(cloudInitScript)},
		{Path: cloudInitEnvPath, Mode: 0o600, Data: []byte(env)},
	}
	return append(seed, r.seed...), nil
}

type partKind int

const (
	partScript partKind = iota
	partCloudConfig
)

type userDataPart struct {
	kind partKind
	body []byte
}

// cloudInitParts splits the user data the way cloud-init's user-data
// processor does: a gzip stream is inflated, a MIME multipart message is
// taken part by part, and each part's type is its content type or the
// starting line that names it.
func cloudInitParts(userData []byte) ([]userDataPart, []string) {
	var warnings []string
	if len(userData) >= 2 && userData[0] == 0x1f && userData[1] == 0x8b {
		inflated, err := gunzip(userData)
		if err != nil {
			return nil, []string{"Failed decompressing user data: " + err.Error()}
		}
		userData = inflated
	}
	if len(bytes.TrimSpace(userData)) == 0 {
		return nil, nil
	}
	if msg, err := mail.ReadMessage(bytes.NewReader(userData)); err == nil {
		mediaType, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
		if err == nil && strings.HasPrefix(mediaType, "multipart/") {
			var parts []userDataPart
			mr := multipart.NewReader(msg.Body, params["boundary"])
			for {
				p, err := mr.NextPart()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					warnings = append(warnings, "Failed reading a MIME part of the user data: "+err.Error())
					break
				}
				body, err := io.ReadAll(p)
				if err != nil {
					warnings = append(warnings, "Failed reading a MIME part of the user data: "+err.Error())
					break
				}
				if strings.EqualFold(p.Header.Get("Content-Transfer-Encoding"), "base64") {
					decoded, err := base64.StdEncoding.DecodeString(string(bytes.Join(bytes.Fields(body), nil)))
					if err != nil {
						warnings = append(warnings, "Failed decoding a base64 MIME part: "+err.Error())
						continue
					}
					body = decoded
				}
				partType, _, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
				part, warning := classifyPart(partType, body)
				if warning != "" {
					warnings = append(warnings, warning)
					continue
				}
				parts = append(parts, part)
			}
			return parts, warnings
		}
	}
	part, warning := classifyPart("", userData)
	if warning != "" {
		return nil, []string{warning}
	}
	return []userDataPart{part}, nil
}

func classifyPart(contentType string, body []byte) (userDataPart, string) {
	switch {
	case contentType == "text/cloud-config" || bytes.HasPrefix(body, []byte("#cloud-config")):
		return userDataPart{kind: partCloudConfig, body: body}, ""
	case contentType == "text/x-shellscript" || bytes.HasPrefix(body, []byte("#!")):
		return userDataPart{kind: partScript, body: body}, ""
	}
	kind := contentType
	if kind == "" {
		kind = string(bytes.SplitN(body, []byte("\n"), 2)[0])
	}
	return userDataPart{}, fmt.Sprintf("Unhandled non-multipart (%s) userdata: the simulator's cloud-init runs cloud-config and shell scripts", kind)
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	return io.ReadAll(zr)
}

// mergeCloudConfig folds a later cloud-config part into an earlier one with
// cloud-init's default merge: mappings merge key by key and anything else the
// later part names replaces the earlier value.
func mergeCloudConfig(dst, src *yaml.Node) {
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		*dst = *src
		return
	}
	for i := 0; i+1 < len(src.Content); i += 2 {
		key, value := src.Content[i], src.Content[i+1]
		replaced := false
		for j := 0; j+1 < len(dst.Content); j += 2 {
			if dst.Content[j].Value == key.Value {
				mergeCloudConfig(dst.Content[j+1], value)
				replaced = true
				break
			}
		}
		if !replaced {
			dst.Content = append(dst.Content, key, value)
		}
	}
}

// cloudConfig is the part of the cloud-config schema the simulator's
// cloud-init runs.
type cloudConfig struct {
	Bootcmd           []shellCommand `yaml:"bootcmd"`
	Runcmd            []shellCommand `yaml:"runcmd"`
	WriteFiles        []writeFile    `yaml:"write_files"`
	Packages          []packageSpec  `yaml:"packages"`
	PackageUpdate     yamlBool       `yaml:"package_update"`
	PackageUpgrade    yamlBool       `yaml:"package_upgrade"`
	Users             *[]userSpec    `yaml:"users"`
	Groups            []groupSpec    `yaml:"groups"`
	SSHAuthorizedKeys []string       `yaml:"ssh_authorized_keys"`
	FinalMessage      string         `yaml:"final_message"`
	Unhandled         map[string]any `yaml:",inline"`
}

// shellCommand is a bootcmd or runcmd entry: a string runs as a shell line,
// a list as an argument vector.
type shellCommand string

func (c *shellCommand) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		*c = shellCommand(n.Value)
		return nil
	case yaml.SequenceNode:
		args := make([]string, 0, len(n.Content))
		for _, a := range n.Content {
			if a.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: a command's arguments are strings", a.Line)
			}
			args = append(args, shellQuote(a.Value))
		}
		*c = shellCommand(strings.Join(args, " "))
		return nil
	}
	return fmt.Errorf("line %d: a command is a string or a list", n.Line)
}

// yamlBool reads a boolean the way cloud-init's YAML 1.1 loader does.
type yamlBool bool

func (b *yamlBool) UnmarshalYAML(n *yaml.Node) error {
	switch strings.ToLower(n.Value) {
	case "true", "yes", "on", "y":
		*b = true
	case "false", "no", "off", "n", "", "null", "~":
		*b = false
	default:
		return fmt.Errorf("line %d: %q is not a boolean", n.Line, n.Value)
	}
	return nil
}

// fileMode reads permissions the way cloud-init does: YAML 1.1 reads an
// unquoted leading-zero number as octal, and a string is octal text.
type fileMode uint32

func (m *fileMode) UnmarshalYAML(n *yaml.Node) error {
	v := strings.TrimPrefix(strings.TrimPrefix(n.Value, "0o"), "0O")
	base := 8
	if n.Tag == "!!int" && !strings.HasPrefix(n.Value, "0") {
		base = 10
	}
	parsed, err := strconv.ParseUint(v, base, 32)
	if err != nil {
		return fmt.Errorf("line %d: permissions %q: %w", n.Line, n.Value, err)
	}
	*m = fileMode(parsed)
	return nil
}

type writeFile struct {
	Path        string    `yaml:"path"`
	Content     string    `yaml:"content"`
	Encoding    string    `yaml:"encoding"`
	Owner       string    `yaml:"owner"`
	Permissions *fileMode `yaml:"permissions"`
	Append      yamlBool  `yaml:"append"`
	Defer       yamlBool  `yaml:"defer"`
	Source      any       `yaml:"source"`
}

// packageSpec is a package name, or a [name, version] pair.
type packageSpec string

func (p *packageSpec) UnmarshalYAML(n *yaml.Node) error {
	switch {
	case n.Kind == yaml.ScalarNode:
		*p = packageSpec(n.Value)
	case n.Kind == yaml.SequenceNode && len(n.Content) == 2:
		*p = packageSpec(n.Content[0].Value + "=" + n.Content[1].Value)
	default:
		return fmt.Errorf("line %d: a package is a name or a [name, version] pair", n.Line)
	}
	return nil
}

// stringList is a list, or a comma-separated string, of names.
type stringList []string

func (l *stringList) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		for _, s := range strings.Split(n.Value, ",") {
			if s = strings.TrimSpace(s); s != "" {
				*l = append(*l, s)
			}
		}
		return nil
	case yaml.SequenceNode:
		for _, c := range n.Content {
			*l = append(*l, c.Value)
		}
		return nil
	}
	return fmt.Errorf("line %d: expected a list or a comma-separated string", n.Line)
}

// sudoRules is a user's sudo: a rule, a list of rules, or false for none.
type sudoRules []string

func (s *sudoRules) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Tag == "!!bool" || n.Tag == "!!null" || strings.EqualFold(n.Value, "false") {
			*s = nil
			return nil
		}
		*s = sudoRules{n.Value}
		return nil
	case yaml.SequenceNode:
		for _, c := range n.Content {
			*s = append(*s, c.Value)
		}
		return nil
	}
	return fmt.Errorf("line %d: sudo is a rule, a list of rules or false", n.Line)
}

type userSpec struct {
	Default           bool
	Name              string     `yaml:"name"`
	Gecos             string     `yaml:"gecos"`
	Homedir           string     `yaml:"homedir"`
	Shell             string     `yaml:"shell"`
	PrimaryGroup      string     `yaml:"primary_group"`
	Groups            stringList `yaml:"groups"`
	Sudo              sudoRules  `yaml:"sudo"`
	LockPasswd        *yamlBool  `yaml:"lock_passwd"`
	Passwd            string     `yaml:"passwd"`
	HashedPasswd      string     `yaml:"hashed_passwd"`
	SSHAuthorizedKeys []string   `yaml:"ssh_authorized_keys"`
	UID               string     `yaml:"uid"`
	System            yamlBool   `yaml:"system"`
}

func (u *userSpec) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value == "default" {
			u.Default = true
		} else {
			u.Name = n.Value
		}
		return nil
	}
	type plain userSpec
	return n.Decode((*plain)(u))
}

type groupSpec struct {
	Name    string
	Members []string
}

func (g *groupSpec) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		g.Name = n.Value
		return nil
	case yaml.MappingNode:
		if len(n.Content) != 2 {
			return fmt.Errorf("line %d: a group entry names one group", n.Line)
		}
		g.Name = n.Content[0].Value
		var members stringList
		if err := n.Content[1].Decode(&members); err != nil {
			return err
		}
		g.Members = members
		return nil
	}
	return fmt.Errorf("line %d: a group is a name or a mapping of a name to its members", n.Line)
}

// cloudInitRender collects the files the modules run from.
type cloudInitRender struct {
	dir      string
	seed     []sim.SeedFile
	warnings []string
	payloads int
}

func (r *cloudInitRender) warn(s string) { r.warnings = append(r.warnings, s) }

// decode reads the document key by key, as cloud-init's modules each read
// their own key: a key whose value does not fit the schema is reported and
// its module skipped, and the others run.
func (r *cloudInitRender) decode(doc *yaml.Node, cfg *cloudConfig) {
	if doc.Kind != yaml.MappingNode {
		r.warn("Invalid cloud-config provided: the document is not a mapping")
		return
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		pair := &yaml.Node{Kind: yaml.MappingNode, Content: doc.Content[i : i+2]}
		var probe cloudConfig
		if err := pair.Decode(&probe); err != nil {
			r.warn(fmt.Sprintf("Invalid cloud-config provided: %s: %v", doc.Content[i].Value, err))
			continue
		}
		if err := pair.Decode(cfg); err != nil {
			r.warn(fmt.Sprintf("Invalid cloud-config provided: %s: %v", doc.Content[i].Value, err))
		}
	}
}

func (r *cloudInitRender) file(rel string, mode uint32, data []byte) {
	r.seed = append(r.seed, sim.SeedFile{Path: path.Join(r.dir, rel), Mode: int64(mode), Data: data})
}

func (r *cloudInitRender) payload(data []byte) string {
	r.payloads++
	rel := fmt.Sprintf("payloads/%d", r.payloads)
	r.file(rel, 0o600, data)
	return path.Join(r.dir, rel)
}

func shellScript(commands []shellCommand) []byte {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	for _, c := range commands {
		b.WriteString(string(c))
		b.WriteString("\n")
	}
	return []byte(b.String())
}

func (r *cloudInitRender) render(cfg cloudConfig, ci CloudInit) {
	for key := range cfg.Unhandled {
		r.warn(fmt.Sprintf("the simulator's cloud-init does not run the %s module; the key is ignored", key))
	}
	if len(cfg.Bootcmd) > 0 {
		r.file("bootcmd", 0o700, shellScript(cfg.Bootcmd))
	}
	if len(cfg.Runcmd) > 0 {
		r.file("scripts/runcmd", 0o700, shellScript(cfg.Runcmd))
	}

	var now, deferred strings.Builder
	for _, f := range cfg.WriteFiles {
		if f.Path == "" {
			r.warn("write_files: an entry names no path")
			continue
		}
		if f.Source != nil {
			r.warn("write_files: " + f.Path + ": a source URI is not fetched")
			continue
		}
		content, err := decodeWriteFile(f.Content, f.Encoding)
		if err != nil {
			r.warn("write_files: " + f.Path + ": " + err.Error())
			continue
		}
		owner := f.Owner
		if owner == "" {
			owner = "root:root"
		}
		mode := fileMode(0o644)
		if f.Permissions != nil {
			mode = *f.Permissions
		}
		appendFlag := "0"
		if f.Append {
			appendFlag = "1"
		}
		line := fmt.Sprintf("ci_write_file %s %s %s %s %s\n", shellQuote(f.Path), shellQuote(r.payload(content)),
			shellQuote(owner), fmt.Sprintf("%04o", uint32(mode)), appendFlag)
		if f.Defer {
			deferred.WriteString(line)
		} else {
			now.WriteString(line)
		}
	}
	if now.Len() > 0 {
		r.file("write-files", 0o600, []byte(now.String()))
	}
	if deferred.Len() > 0 {
		r.file("write-files-deferred", 0o600, []byte(deferred.String()))
	}

	var users strings.Builder
	for _, g := range cfg.Groups {
		args := []string{shellQuote(g.Name)}
		for _, m := range g.Members {
			args = append(args, shellQuote(m))
		}
		users.WriteString("ci_group " + strings.Join(args, " ") + "\n")
	}
	userList := []userSpec{{Default: true}}
	if cfg.Users != nil {
		userList = *cfg.Users
	}
	hasDefault := false
	for _, u := range userList {
		if u.Default {
			hasDefault = true
			users.WriteString("ci_default_user\n")
			continue
		}
		if u.Name == "" {
			r.warn("users: an entry names no user")
			continue
		}
		lock := "1"
		if u.LockPasswd != nil && !*u.LockPasswd {
			lock = "0"
		}
		hash := u.HashedPasswd
		if hash == "" {
			hash = u.Passwd
		}
		system := "0"
		if u.System {
			system = "1"
		}
		users.WriteString("ci_user " + strings.Join([]string{
			shellQuote(u.Name), shellQuote(u.Gecos), shellQuote(u.Homedir), shellQuote(u.Shell),
			shellQuote(u.PrimaryGroup), shellQuote(u.UID), system, lock, shellQuote(hash),
			shellQuote(strings.Join(u.Groups, " ")), shellQuote(strings.Join(u.Sudo, "\n")),
		}, " ") + "\n")
		if len(u.SSHAuthorizedKeys) > 0 {
			users.WriteString("ci_keys " + shellQuote(u.Name) + " " + shellQuote(r.payload([]byte(strings.Join(u.SSHAuthorizedKeys, "\n")+"\n"))) + "\n")
		}
	}
	if users.Len() > 0 {
		r.file("users-groups", 0o600, []byte(users.String()))
	}

	keys := append(append([]string(nil), ci.DefaultUserKeys...), cfg.SSHAuthorizedKeys...)
	if len(keys) > 0 {
		keyFile := shellQuote(r.payload([]byte(strings.Join(keys, "\n") + "\n")))
		var ssh strings.Builder
		user := "NONE"
		if hasDefault {
			ssh.WriteString("ci_keys \"$ci_default_name\" " + keyFile + "\n")
			user = "$ci_default_name"
		}
		ssh.WriteString("ci_root_keys \"" + user + "\" " + keyFile + "\n")
		r.file("ssh", 0o600, []byte(ssh.String()))
	}

	if len(cfg.Packages) > 0 || cfg.PackageUpdate || cfg.PackageUpgrade {
		args := []string{boolFlag(bool(cfg.PackageUpdate)), boolFlag(bool(cfg.PackageUpgrade))}
		for _, p := range cfg.Packages {
			args = append(args, shellQuote(string(p)))
		}
		r.file("packages", 0o600, []byte("ci_packages "+strings.Join(args, " ")+"\n"))
	}
}

func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// decodeWriteFile decodes a write_files entry's content by its encoding, as
// cloud-init's write_files module does.
func decodeWriteFile(content, encoding string) ([]byte, error) {
	switch strings.ToLower(encoding) {
	case "", "text/plain":
		return []byte(content), nil
	case "b64", "base64":
		return base64.StdEncoding.DecodeString(strings.Join(strings.Fields(content), ""))
	case "gz", "gzip":
		return gunzip([]byte(content))
	case "gz+base64", "gzip+base64", "gz+b64", "gzip+b64":
		raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(content), ""))
		if err != nil {
			return nil, err
		}
		return gunzip(raw)
	}
	return nil, fmt.Errorf("unknown encoding %q", encoding)
}
