package main

import (
	"database/sql"
	"net/http"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// IAM keeps every account's identities apart. A store keyed by an entity's
// name holds the management account's entities in its own table and each
// member account's in a table of its own, because a name is unique only within
// its account. Stores keyed by an access key ID, a certificate ID or an ARN
// stay shared: AWS makes those keys unique across accounts, and an entry that
// belongs to a user records the user's account.

var (
	iamStoreDB       *sql.DB
	iamAccountScopes []interface{ reset() }
)

type iamAccountStore[T any] struct {
	table    string
	mgmt     *sim.Store[T]
	announce bool
	mu       sync.Mutex
	members  map[string]sim.Store[T]
}

func newIAMAccountStore[T any](table string, mgmt *sim.Store[T], announce bool) *iamAccountStore[T] {
	s := &iamAccountStore[T]{table: table, mgmt: mgmt, announce: announce}
	iamAccountScopes = append(iamAccountScopes, s)
	return s
}

func (s *iamAccountStore[T]) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.members = nil
}

// in is the store of account's entities. A member account's table is opened
// on first use, which also reopens what a previous run persisted in it.
func (s *iamAccountStore[T]) in(account string) sim.Store[T] {
	if account == "" || account == awsAccountID() {
		return *s.mgmt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if store, ok := s.members[account]; ok {
		return store
	}
	if !iamHostedAccount(account) {
		// An account the simulator does not host holds no entity, and no
		// credential acts in it to write one.
		return sim.NewStateStore[T]()
	}
	store := sim.MakeStore[T](iamStoreDB, s.table+"@"+account)
	if s.announce {
		store = sim.Announcing(store, iamChanges)
	}
	if s.members == nil {
		s.members = map[string]sim.Store[T]{}
	}
	s.members[account] = store
	return store
}

var (
	iamRolesScoped              = newIAMAccountStore("iam_roles", &iamRoles, true)
	iamRolePoliciesScoped       = newIAMAccountStore("iam_role_policies", &iamRolePolicies, true)
	iamAttachedPoliciesScoped   = newIAMAccountStore("iam_attached_policies", &iamAttachedPolicies, true)
	iamInstanceProfilesScoped   = newIAMAccountStore("iam_instance_profiles", &iamInstanceProfiles, false)
	iamInstanceProfileTagScoped = newIAMAccountStore("iam_instance_profile_tags", &iamInstanceProfileTag, false)
	iamUsersScoped              = newIAMAccountStore("iam_users", &iamUsers, false)
	iamUserPoliciesScoped       = newIAMAccountStore("iam_user_policies", &iamUserPolicies, false)
	iamUserAttachedScoped       = newIAMAccountStore("iam_user_attached_policies", &iamUserAttached, false)
	iamUserBoundariesScoped     = newIAMAccountStore("iam_user_boundaries", &iamUserBoundaries, false)
	iamGroupsScoped             = newIAMAccountStore("iam_groups", &iamGroups, false)
	iamGroupMembersScoped       = newIAMAccountStore("iam_group_members", &iamGroupMembers, false)
	iamGroupPoliciesScoped      = newIAMAccountStore("iam_group_policies", &iamGroupPolicies, false)
	iamGroupAttachedScoped      = newIAMAccountStore("iam_group_attached_policies", &iamGroupAttached, false)
	iamLoginProfilesScoped      = newIAMAccountStore("iam_login_profiles", &iamLoginProfiles, false)
	iamPasswordPoliciesScoped   = newIAMAccountStore("iam_password_policies", &iamPasswordPolicies, false)
	iamAccountAliasesScoped     = newIAMAccountStore("iam_account_aliases", &iamAccountAliases, false)
	iamServerCertsScoped        = newIAMAccountStore("iam_server_certificates", &iamServerCerts, false)
	iamSLRsScoped               = newIAMAccountStore("iam_slrs", &iamSLRs, false)
	iamCredReportGenScoped      = newIAMAccountStore("iam_cred_report_gen", &iamCredReportGen, false)
)

func iamRolesIn(a string) sim.Store[IAMRole]              { return iamRolesScoped.in(a) }
func iamRolePoliciesIn(a string) sim.Store[IAMRolePolicy] { return iamRolePoliciesScoped.in(a) }
func iamAttachedPoliciesIn(a string) sim.Store[IAMAttachedPolicy] {
	return iamAttachedPoliciesScoped.in(a)
}
func iamInstanceProfilesIn(a string) sim.Store[IAMInstanceProfile] {
	return iamInstanceProfilesScoped.in(a)
}
func iamInstanceProfileTagIn(a string) sim.Store[IAMInstanceProfileTagSet] {
	return iamInstanceProfileTagScoped.in(a)
}
func iamUsersIn(a string) sim.Store[IAMUser]              { return iamUsersScoped.in(a) }
func iamUserPoliciesIn(a string) sim.Store[IAMUserPolicy] { return iamUserPoliciesScoped.in(a) }
func iamUserAttachedIn(a string) sim.Store[IAMUserAttachedPolicy] {
	return iamUserAttachedScoped.in(a)
}
func iamUserBoundariesIn(a string) sim.Store[IAMUserBoundary] { return iamUserBoundariesScoped.in(a) }
func iamGroupsIn(a string) sim.Store[IAMGroup]                { return iamGroupsScoped.in(a) }
func iamGroupMembersIn(a string) sim.Store[IAMGroupMember]    { return iamGroupMembersScoped.in(a) }
func iamGroupPoliciesIn(a string) sim.Store[IAMGroupPolicy]   { return iamGroupPoliciesScoped.in(a) }
func iamGroupAttachedIn(a string) sim.Store[IAMGroupAttachedPolicy] {
	return iamGroupAttachedScoped.in(a)
}
func iamLoginProfilesIn(a string) sim.Store[IAMLoginProfile] { return iamLoginProfilesScoped.in(a) }
func iamPasswordPoliciesIn(a string) sim.Store[IAMPasswordPolicy] {
	return iamPasswordPoliciesScoped.in(a)
}
func iamAccountAliasesIn(a string) sim.Store[string]            { return iamAccountAliasesScoped.in(a) }
func iamServerCertsIn(a string) sim.Store[IAMServerCertificate] { return iamServerCertsScoped.in(a) }
func iamSLRsIn(a string) sim.Store[IAMServiceLinkedRole]        { return iamSLRsScoped.in(a) }
func iamCredReportGenIn(a string) sim.Store[IAMAccountFeature]  { return iamCredReportGenScoped.in(a) }

// iamOpenAccountStores points the member-account tables at a build's
// database, dropping those a previous build opened.
func iamOpenAccountStores(srv *sim.Server) {
	iamStoreDB = srv.DB()
	for _, scope := range iamAccountScopes {
		scope.reset()
	}
}

// iamRequestAccount is the account a request acts in: the account of the
// credential that signed it, and the management account for an unsigned one.
func iamRequestAccount(r *http.Request) string {
	if acct := iamAccessKeyAccount(iamAccessKeyIDFromRequest(r)); acct != "" {
		return acct
	}
	return awsAccountID()
}

// iamAccessKeyAccount is the account whose principal holds akid, empty for a
// key no principal holds.
func iamAccessKeyAccount(akid string) string {
	if akid == "" {
		return ""
	}
	if iamTempCreds != nil {
		if tc, ok := iamTempCreds.Get(akid); ok {
			return iamArnAccount(tc.PrincipalArn)
		}
	}
	if iamAccessKeys != nil {
		if key, ok := iamAccessKeys.Get(akid); ok {
			return key.account()
		}
	}
	return ""
}

// iamIsManagementAccount reports whether account is the organization's
// management account, which the simulator's own account is.
func iamIsManagementAccount(account string) bool {
	return account == "" || account == awsAccountID()
}

// iamHostedAccount reports whether the simulator hosts account: the
// management account and the organization's member accounts.
func iamHostedAccount(account string) bool {
	if iamIsManagementAccount(account) {
		return true
	}
	if orgAccounts == nil {
		return false
	}
	_, ok := orgAccounts.Get(account)
	return ok
}

// iamRoleByArn is the role an ARN names, read from the account the ARN
// carries.
func iamRoleByArn(arn string) (IAMRole, bool) {
	acct := iamArnAccount(arn)
	if acct == "" || !strings.Contains(arn, ":role/") {
		return IAMRole{}, false
	}
	return iamRolesIn(acct).Get(iamRoleNameFromArn(arn))
}

func iamRootArn(account string) string { return "arn:aws:iam::" + account + ":root" }

func iamIsRootArn(arn string) bool {
	return strings.HasPrefix(arn, "arn:aws:iam::") && strings.HasSuffix(arn, ":root")
}

func iamIsServiceLinkedRole(role IAMRole) bool {
	return strings.HasPrefix(role.Path, "/aws-service-role/")
}
