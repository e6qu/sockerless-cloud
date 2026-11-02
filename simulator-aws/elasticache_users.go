package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// ElastiCache RBAC users and user groups. Every cache a user group is
// attached to runs the group's users as engine users (elasticache_auth.go),
// so a change to a user or a group reaches those caches' engines.

const (
	ecAuthPassword   = "password"
	ecAuthNoPassword = "no-password"
	ecAuthIAM        = "iam"
	ecDefaultUserID  = "default"
	ecDefaultName    = "default"
)

// ElastiCache configures a default user with user ID and user name default
// and an access string that permits every command and key.
func ecSeedDefaultUser() {
	if _, ok := ecUsers.Get(ecDefaultUserID); ok {
		return
	}
	ecUsers.Put(ecDefaultUserID, ECUser{
		UserId: ecDefaultUserID, UserName: ecDefaultName, Status: "active", Engine: "redis",
		MinimumEngineVersion: "6.0", AccessString: "on ~* +@all", NoPasswordRequired: true,
		AuthenticationType: ecAuthNoPassword, ARN: ecUserARN(ecDefaultUserID),
	})
}

func ecUserAuthType(u ECUser) string {
	switch {
	case u.AuthenticationType != "":
		return u.AuthenticationType
	case u.NoPasswordRequired:
		return ecAuthNoPassword
	}
	return ecAuthPassword
}

func ecUserPasswords(u ECUser) ([]string, error) {
	if len(u.Passwords) == 0 {
		return nil, nil
	}
	encoded, err := ecOpenToken(u.Passwords)
	if err != nil {
		return nil, err
	}
	var passwords []string
	if err := json.Unmarshal([]byte(encoded), &passwords); err != nil {
		return nil, fmt.Errorf("decode the passwords of user %s: %w", u.UserId, err)
	}
	return passwords, nil
}

// The groups a user belongs to, which DescribeUsers reports.
func ecGroupsOfUser(id string) []string {
	var groups []string
	for _, g := range ecUserGroups.List() {
		if slices.Contains(g.UserIds, id) {
			groups = append(groups, g.UserGroupId)
		}
	}
	sort.Strings(groups)
	return groups
}

// Create, Modify and Delete flatten the User shape into their Result;
// DescribeUsers wraps each in a <member>.
func renderECUserBody(u ECUser) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<UserId>%s</UserId>", xmlEscape(u.UserId))
	fmt.Fprintf(&b, "<UserName>%s</UserName>", xmlEscape(u.UserName))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(u.Status))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(u.Engine))
	if u.MinimumEngineVersion != "" {
		fmt.Fprintf(&b, "<MinimumEngineVersion>%s</MinimumEngineVersion>", xmlEscape(u.MinimumEngineVersion))
	}
	fmt.Fprintf(&b, "<AccessString>%s</AccessString>", xmlEscape(u.AccessString))
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(u.ARN))
	b.WriteString("<UserGroupIds>")
	for _, id := range ecGroupsOfUser(u.UserId) {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(id))
	}
	b.WriteString("</UserGroupIds>")
	fmt.Fprintf(&b, "<Authentication><Type>%s</Type><PasswordCount>%d</PasswordCount></Authentication>", ecUserAuthType(u), u.PasswordCount)
	return b.String()
}

// The RBAC limits: 16 to 128 printable characters, none of , " / @.
func ecValidUserPassword(password string) bool {
	return ecValidAuthToken(password) && !strings.ContainsAny(password, `,"/@`)
}

// An access string carries no password and no reset: ElastiCache manages a
// user's passwords itself.
func ecValidateAccessString(access string) string {
	rule, err := ecSplitAccessString(access)
	if err != nil {
		return err.Error()
	}
	for _, arg := range rule {
		switch {
		case strings.EqualFold(arg, "reset"):
			return "An access string cannot use reset."
		case strings.HasPrefix(arg, ">"), strings.HasPrefix(arg, "<"), strings.HasPrefix(arg, "#"), strings.HasPrefix(arg, "!"):
			return "An access string cannot set passwords; set them with Passwords."
		}
	}
	return ""
}

// ecRequestedAuthentication reads a CreateUser or ModifyUser request's
// authentication: its type and passwords, and whether it names any.
func ecRequestedAuthentication(r *http.Request) (authType string, passwords []string, named bool, problem string) {
	passwords = ecParseUserIds(r, "Passwords")
	modePasswords := ecParseUserIds(r, "AuthenticationMode.Passwords")
	mode := r.FormValue("AuthenticationMode.Type")
	noPassword := r.FormValue("NoPasswordRequired")
	switch mode {
	case "":
		switch {
		case strings.EqualFold(noPassword, "true"):
			if len(passwords) > 0 {
				return "", nil, true, "Passwords and NoPasswordRequired cannot be combined."
			}
			return ecAuthNoPassword, nil, true, ""
		case len(passwords) > 0:
			authType = ecAuthPassword
		default:
			return "", nil, noPassword != "", ""
		}
	case ecAuthIAM, "no-password-required":
		if len(passwords) > 0 || len(modePasswords) > 0 {
			return "", nil, true, fmt.Sprintf("AuthenticationMode type %s takes no passwords.", mode)
		}
		if mode == ecAuthIAM {
			return ecAuthIAM, nil, true, ""
		}
		return ecAuthNoPassword, nil, true, ""
	case ecAuthPassword:
		authType = ecAuthPassword
		passwords = append(passwords, modePasswords...)
	default:
		return "", nil, true, fmt.Sprintf("AuthenticationMode type %q is not one of password, no-password-required or iam.", mode)
	}
	if len(passwords) == 0 || len(passwords) > 2 {
		return "", nil, true, "A user has one or two passwords."
	}
	for _, password := range passwords {
		if !ecValidUserPassword(password) {
			return "", nil, true, `Passwords must be 16 to 128 printable characters and cannot contain , " / or @.`
		}
	}
	return authType, passwords, true, ""
}

func ecApplyAuthentication(u *ECUser, authType string, passwords []string) error {
	u.AuthenticationType = authType
	u.NoPasswordRequired = authType == ecAuthNoPassword
	u.PasswordCount = len(passwords)
	u.Passwords = nil
	if len(passwords) == 0 {
		return nil
	}
	encoded, err := json.Marshal(passwords)
	if err != nil {
		return err
	}
	u.Passwords, err = ecSealToken(string(encoded))
	return err
}

func handleECCreateUser(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	invalid := func(code, message string) { ecErrorXML(w, code, message, http.StatusBadRequest, requestID) }
	id := strings.ToLower(r.FormValue("UserId"))
	name := r.FormValue("UserName")
	if id == "" || name == "" {
		invalid("MissingParameter", "UserId and UserName are required")
		return
	}
	if _, ok := ecUsers.Get(id); ok {
		invalid("UserAlreadyExists", fmt.Sprintf("User %s already exists.", id))
		return
	}
	engine := strings.ToLower(r.FormValue("Engine"))
	if engine != "redis" && engine != "valkey" {
		invalid("InvalidParameterValue", "Engine must be valkey or redis.")
		return
	}
	access := r.FormValue("AccessString")
	if problem := ecValidateAccessString(access); problem != "" {
		invalid("InvalidParameterValue", problem)
		return
	}
	authType, passwords, named, problem := ecRequestedAuthentication(r)
	switch {
	case problem != "":
		invalid("InvalidParameterCombination", problem)
		return
	case !named:
		invalid("InvalidParameterValue", "A user needs Passwords, NoPasswordRequired or an AuthenticationMode.")
		return
	case authType == ecAuthIAM && id != name:
		invalid("InvalidParameterValue", "An IAM-enabled user's UserId and UserName must be identical.")
		return
	case authType == ecAuthNoPassword && engine == "valkey":
		invalid("InvalidParameterCombination", "A Valkey user authenticates with a password or IAM.")
		return
	}
	u := ECUser{
		UserId:               id,
		UserName:             name,
		Status:               "active",
		Engine:               engine,
		MinimumEngineVersion: "6.0",
		AccessString:         access,
		ARN:                  ecUserARN(id),
		Tags:                 parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	if err := ecApplyAuthentication(&u, authType, passwords); err != nil {
		ecErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, requestID)
		return
	}
	ecUsers.Put(id, u)
	ecXMLResponse(w, "CreateUser", renderECUserBody(u), requestID)
}

func handleECDescribeUsers(w http.ResponseWriter, r *http.Request) {
	wanted := strings.ToLower(r.FormValue("UserId"))
	engine := strings.ToLower(r.FormValue("Engine"))
	var b strings.Builder
	b.WriteString("<Users>")
	matched := false
	for _, u := range ecUsers.List() {
		if (wanted != "" && u.UserId != wanted) || (engine != "" && u.Engine != engine) {
			continue
		}
		matched = true
		b.WriteString("<member>")
		b.WriteString(renderECUserBody(u))
		b.WriteString("</member>")
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "UserNotFound", fmt.Sprintf("User %s not found.", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</Users>")
	ecXMLResponse(w, "DescribeUsers", b.String(), sim.RequestID(r.Context()))
}

// ecMutableUser finds a user a request may change: one that exists, is not
// the default user ElastiCache configures, and is active.
func ecMutableUser(w http.ResponseWriter, r *http.Request) (ECUser, bool) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("UserId"))
	u, ok := ecUsers.Get(id)
	switch {
	case !ok:
		ecErrorXML(w, "UserNotFound", fmt.Sprintf("User %s not found.", id), http.StatusNotFound, requestID)
	case id == ecDefaultUserID:
		ecErrorXML(w, "InvalidParameterValue", "The default user cannot be modified or deleted.", http.StatusBadRequest, requestID)
	case u.Status != "active":
		ecErrorXML(w, "InvalidUserState", fmt.Sprintf("User %s is %s, not active.", id, u.Status), http.StatusBadRequest, requestID)
	default:
		return u, true
	}
	return ECUser{}, false
}

func handleECModifyUser(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	invalid := func(code, message string) { ecErrorXML(w, code, message, http.StatusBadRequest, requestID) }
	u, ok := ecMutableUser(w, r)
	if !ok {
		return
	}
	next := u
	if v := r.FormValue("AccessString"); v != "" {
		next.AccessString = v
	}
	if v := r.FormValue("AppendAccessString"); v != "" {
		next.AccessString = strings.TrimSpace(next.AccessString + " " + v)
	}
	if problem := ecValidateAccessString(next.AccessString); problem != "" {
		invalid("InvalidParameterValue", problem)
		return
	}
	if v := strings.ToLower(r.FormValue("Engine")); v != "" {
		if v != "redis" && v != "valkey" {
			invalid("InvalidParameterValue", "Engine must be valkey or redis.")
			return
		}
		next.Engine = v
	}
	authType, passwords, named, problem := ecRequestedAuthentication(r)
	if problem != "" {
		invalid("InvalidParameterCombination", problem)
		return
	}
	if named {
		if authType == ecAuthIAM && next.UserId != next.UserName {
			invalid("InvalidParameterValue", "An IAM-enabled user's UserId and UserName must be identical.")
			return
		}
		if err := ecApplyAuthentication(&next, authType, passwords); err != nil {
			ecErrorXML(w, "InternalFailure", err.Error(), http.StatusInternalServerError, requestID)
			return
		}
	}
	if ecUserAuthType(next) == ecAuthNoPassword && next.Engine == "valkey" {
		invalid("InvalidParameterCombination", "A Valkey user authenticates with a password or IAM.")
		return
	}
	groups := ecGroupsOfUser(u.UserId)
	if message, ok := ecUserFitsGroups(next, groups); !ok {
		invalid("InvalidParameterCombination", message)
		return
	}
	affected := len(ecCachesOfGroups(groups)) > 0
	if affected {
		next.Status = "modifying"
	}
	ecUsers.Put(u.UserId, next)
	if affected {
		ecReapplyUsers(groups, func() {
			ecUsers.Update(u.UserId, func(stored *ECUser) { stored.Status = "active" })
		})
	}
	ecXMLResponse(w, "ModifyUser", renderECUserBody(next), requestID)
}

// A user ElastiCache deletes leaves every user group it belongs to.
func handleECDeleteUser(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	u, ok := ecMutableUser(w, r)
	if !ok {
		return
	}
	groups := ecGroupsOfUser(u.UserId)
	if u.UserName == ecDefaultName && len(groups) > 0 {
		ecErrorXML(w, "DefaultUserAssociatedToUserGroup",
			fmt.Sprintf("User %s is the default user of user group %s.", u.UserId, groups[0]), http.StatusBadRequest, requestID)
		return
	}
	for _, id := range groups {
		ecUserGroups.Update(id, func(g *ECUserGroup) {
			g.UserIds = slices.DeleteFunc(g.UserIds, func(member string) bool { return member == u.UserId })
		})
	}
	u.Status = "deleting"
	ecUsers.Put(u.UserId, u)
	ecReapplyUsers(groups, func() { ecUsers.Delete(u.UserId) })
	ecXMLResponse(w, "DeleteUser", renderECUserBody(u), requestID)
}

func renderECUserGroupBody(g ECUserGroup) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<UserGroupId>%s</UserGroupId>", xmlEscape(g.UserGroupId))
	fmt.Fprintf(&b, "<Status>%s</Status>", xmlEscape(g.Status))
	fmt.Fprintf(&b, "<Engine>%s</Engine>", xmlEscape(g.Engine))
	if g.MinimumEngineVersion != "" {
		fmt.Fprintf(&b, "<MinimumEngineVersion>%s</MinimumEngineVersion>", xmlEscape(g.MinimumEngineVersion))
	}
	b.WriteString("<UserIds>")
	for _, id := range g.UserIds {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(id))
	}
	b.WriteString("</UserIds>")
	replicationGroups, serverlessCaches := ecCachesUsingGroup(g.UserGroupId)
	b.WriteString("<ReplicationGroups>")
	for _, id := range replicationGroups {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(id))
	}
	b.WriteString("</ReplicationGroups>")
	b.WriteString("<ServerlessCaches>")
	for _, name := range serverlessCaches {
		fmt.Fprintf(&b, "<member>%s</member>", xmlEscape(name))
	}
	b.WriteString("</ServerlessCaches>")
	fmt.Fprintf(&b, "<ARN>%s</ARN>", xmlEscape(g.ARN))
	return b.String()
}

func ecParseUserIds(r *http.Request, field string) []string {
	var ids []string
	for n := 1; n <= 100; n++ {
		v := r.FormValue(fmt.Sprintf("%s.member.%d", field, n))
		if v == "" {
			break
		}
		ids = append(ids, v)
	}
	return ids
}

// ecCheckGroupMembers checks a user group's members as ElastiCache does: each
// exists, no two share a user name, a Redis OSS group holds a user named
// default and Redis OSS users alone, and a Valkey group holds no user that
// authenticates without a password. It answers the fault code and message
// of the first member that fails.
func ecCheckGroupMembers(engine string, members []string) (string, string, bool) {
	names := map[string]string{}
	for _, id := range members {
		u, ok := ecUsers.Get(id)
		if !ok {
			return "UserNotFound", fmt.Sprintf("User %s not found.", id), false
		}
		if other, taken := names[u.UserName]; taken {
			return "DuplicateUserName", fmt.Sprintf("Users %s and %s share the user name %s.", other, id, u.UserName), false
		}
		names[u.UserName] = id
		if message, ok := ecUserFitsGroupEngine(u, engine); !ok {
			return "InvalidParameterCombination", message, false
		}
	}
	if _, ok := names[ecDefaultName]; !ok && engine == "redis" {
		return "DefaultUserRequired", "A Redis OSS user group must contain a user named default.", false
	}
	return "", "", true
}

func ecUserFitsGroupEngine(u ECUser, engine string) (string, bool) {
	switch {
	case engine == "redis" && u.Engine != "redis":
		return fmt.Sprintf("User %s runs %s; a Redis OSS user group holds Redis OSS users only.", u.UserId, u.Engine), false
	case engine == "valkey" && ecUserAuthType(u) == ecAuthNoPassword:
		return fmt.Sprintf("User %s authenticates without a password; a Valkey user group holds users that authenticate with a password or IAM.", u.UserId), false
	}
	return "", true
}

func ecUserFitsGroups(u ECUser, groups []string) (string, bool) {
	for _, id := range groups {
		if g, ok := ecUserGroups.Get(id); ok {
			if message, ok := ecUserFitsGroupEngine(u, g.Engine); !ok {
				return message, false
			}
		}
	}
	return "", true
}

func handleECCreateUserGroup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	invalid := func(code, message string) { ecErrorXML(w, code, message, http.StatusBadRequest, requestID) }
	id := strings.ToLower(r.FormValue("UserGroupId"))
	if id == "" {
		invalid("MissingParameter", "UserGroupId is required")
		return
	}
	if _, ok := ecUserGroups.Get(id); ok {
		invalid("UserGroupAlreadyExists", fmt.Sprintf("User group %s already exists.", id))
		return
	}
	engine := strings.ToLower(r.FormValue("Engine"))
	if engine != "redis" && engine != "valkey" {
		invalid("InvalidParameterValue", "Engine must be valkey or redis.")
		return
	}
	members := ecLowerAll(ecParseUserIds(r, "UserIds"))
	if code, message, ok := ecCheckGroupMembers(engine, members); !ok {
		status := http.StatusBadRequest
		if code == "UserNotFound" {
			status = http.StatusNotFound
		}
		ecErrorXML(w, code, message, status, requestID)
		return
	}
	g := ECUserGroup{
		UserGroupId:          id,
		Status:               "active",
		Engine:               engine,
		MinimumEngineVersion: "6.0",
		UserIds:              members,
		ARN:                  ecUserGroupARN(id),
		Tags:                 parseAWSQueryTagMap(r, "Tags.Tag"),
	}
	ecUserGroups.Put(id, g)
	ecXMLResponse(w, "CreateUserGroup", renderECUserGroupBody(g), requestID)
}

func ecLowerAll(values []string) []string {
	for i, value := range values {
		values[i] = strings.ToLower(value)
	}
	return values
}

func handleECDescribeUserGroups(w http.ResponseWriter, r *http.Request) {
	wanted := strings.ToLower(r.FormValue("UserGroupId"))
	var b strings.Builder
	b.WriteString("<UserGroups>")
	matched := false
	for _, g := range ecUserGroups.List() {
		if wanted != "" && g.UserGroupId != wanted {
			continue
		}
		matched = true
		b.WriteString("<member>")
		b.WriteString(renderECUserGroupBody(g))
		b.WriteString("</member>")
	}
	if wanted != "" && !matched {
		ecErrorXML(w, "UserGroupNotFound", fmt.Sprintf("User group %s not found.", wanted), http.StatusNotFound, sim.RequestID(r.Context()))
		return
	}
	b.WriteString("</UserGroups>")
	ecXMLResponse(w, "DescribeUserGroups", b.String(), sim.RequestID(r.Context()))
}

func ecActiveUserGroup(w http.ResponseWriter, r *http.Request) (ECUserGroup, bool) {
	requestID := sim.RequestID(r.Context())
	id := strings.ToLower(r.FormValue("UserGroupId"))
	g, ok := ecUserGroups.Get(id)
	switch {
	case !ok:
		ecErrorXML(w, "UserGroupNotFound", fmt.Sprintf("User group %s not found.", id), http.StatusNotFound, requestID)
	case g.Status != "active":
		ecErrorXML(w, "InvalidUserGroupState", fmt.Sprintf("User group %s is %s, not active.", id, g.Status), http.StatusBadRequest, requestID)
	default:
		return g, true
	}
	return ECUserGroup{}, false
}

func handleECModifyUserGroup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	g, ok := ecActiveUserGroup(w, r)
	if !ok {
		return
	}
	add := ecLowerAll(ecParseUserIds(r, "UserIdsToAdd"))
	remove := ecLowerAll(ecParseUserIds(r, "UserIdsToRemove"))
	members := slices.DeleteFunc(slices.Clone(g.UserIds), func(id string) bool { return slices.Contains(remove, id) })
	for _, id := range add {
		if !slices.Contains(members, id) {
			members = append(members, id)
		}
	}
	if code, message, ok := ecCheckGroupMembers(g.Engine, members); !ok {
		status := http.StatusBadRequest
		if code == "UserNotFound" {
			status = http.StatusNotFound
		}
		ecErrorXML(w, code, message, status, requestID)
		return
	}
	g.UserIds = members
	affected := len(ecCachesOfGroups([]string{g.UserGroupId})) > 0
	if affected {
		g.Status = "modifying"
	}
	ecUserGroups.Put(g.UserGroupId, g)
	if affected {
		ecReapplyUsers([]string{g.UserGroupId}, func() {
			ecUserGroups.Update(g.UserGroupId, func(stored *ECUserGroup) { stored.Status = "active" })
		})
	}
	ecXMLResponse(w, "ModifyUserGroup", renderECUserGroupBody(g), requestID)
}

// A user group leaves every cache before ElastiCache deletes it.
func handleECDeleteUserGroup(w http.ResponseWriter, r *http.Request) {
	requestID := sim.RequestID(r.Context())
	g, ok := ecActiveUserGroup(w, r)
	if !ok {
		return
	}
	if replicationGroups, serverlessCaches := ecCachesUsingGroup(g.UserGroupId); len(replicationGroups)+len(serverlessCaches) > 0 {
		ecErrorXML(w, "InvalidUserGroupState",
			fmt.Sprintf("User group %s is associated with a cache; disassociate it first.", g.UserGroupId), http.StatusBadRequest, requestID)
		return
	}
	g.Status = "deleting"
	ecUserGroups.Delete(g.UserGroupId)
	ecXMLResponse(w, "DeleteUserGroup", renderECUserGroupBody(g), requestID)
}
