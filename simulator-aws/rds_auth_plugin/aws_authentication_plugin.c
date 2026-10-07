/*
 * AWSAuthenticationPlugin for the MySQL 8.0 and MariaDB 11.4 engines behind
 * the simulator's Amazon RDS endpoints. The endpoint validates a user's IAM
 * authentication token and refuses any other login of a user identified with
 * the plugin before it reaches the engine, so the plugin trusts the relayed
 * login the way the PostgreSQL engine's trust method does.
 *
 * The plugin needs no C library: build it with build.sh.
 */

#define MYSQL_AUTHENTICATION_PLUGIN 7
#define PLUGIN_LICENSE_GPL 1
#define CR_ERROR 0
#define CR_OK (-1)
#define EXPORT __attribute__((visibility("default")))

#define PASSWORD_USED_YES 1
#ifdef MARIADB
#define USERNAME_LENGTH 512
#define EXTERNAL_USER_LENGTH (512 + 1)
#else
#define USERNAME_LENGTH 96
#define EXTERNAL_USER_LENGTH 512
#endif

typedef struct plugin_vio {
	int (*read_packet)(struct plugin_vio *vio, unsigned char **buf);
} plugin_vio;

/* The leading members of the engine's MYSQL_SERVER_AUTH_INFO. */
typedef struct server_auth_info {
	const char *user_name;
	unsigned int user_name_length;
	const char *auth_string;
	unsigned long auth_string_length;
	char authenticated_as[USERNAME_LENGTH + 1];
	char external_user[EXTERNAL_USER_LENGTH];
	int password_used;
} server_auth_info;

/* An IAM authentication token is a password, so a refusal reports one used. */
static int authenticate_user(plugin_vio *vio, server_auth_info *info)
{
	unsigned char *response;
	info->password_used = PASSWORD_USED_YES;
	return vio->read_packet(vio, &response) < 0 ? CR_ERROR : CR_OK;
}

static const char name[] = "AWSAuthenticationPlugin";
static const char author[] = "Sockerless";
static const char description[] = "Amazon RDS IAM database authentication";

#ifdef MARIADB

struct st_mysql_auth {
	int interface_version;
	const char *client_auth_plugin;
	int (*authenticate_user)(plugin_vio *vio, server_auth_info *info);
	int (*hash_password)(const char *password, unsigned long password_length, char *hash, unsigned long *hash_length);
	int (*preprocess_hash)(const char *hash, unsigned long hash_length, unsigned char *out, unsigned long *out_length);
};

static struct st_mysql_auth auth = {0x0203, "mysql_native_password", authenticate_user, 0, 0};

struct st_maria_plugin {
	int type;
	void *info;
	const char *name;
	const char *author;
	const char *descr;
	int license;
	int (*init)(void *);
	int (*deinit)(void *);
	unsigned int version;
	void *status_vars;
	void *system_vars;
	const char *version_info;
	unsigned int maturity;
};

EXPORT int _maria_plugin_interface_version_ = 0x010f;
EXPORT int _maria_sizeof_struct_st_plugin_ = sizeof(struct st_maria_plugin);
EXPORT struct st_maria_plugin _maria_plugin_declarations_[] = {
	{MYSQL_AUTHENTICATION_PLUGIN, &auth, name, author, description, PLUGIN_LICENSE_GPL, 0, 0, 0x0100, 0, 0, "1.0", 5},
	{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
};

#else

static int generate_authentication_string(char *outbuf, unsigned int *outbuflen, const char *inbuf, unsigned int inbuflen)
{
	if (inbuflen > *outbuflen)
		return 1;
	for (unsigned int i = 0; i < inbuflen; i++)
		((volatile char *)outbuf)[i] = inbuf[i];
	*outbuflen = inbuflen;
	return 0;
}

static int validate_authentication_string(char *const inbuf, unsigned int buflen)
{
	(void)inbuf;
	(void)buflen;
	return 0;
}

static int set_salt(const char *password, unsigned int password_len, unsigned char *salt, unsigned char *salt_len)
{
	(void)password;
	(void)password_len;
	(void)salt;
	*salt_len = 0;
	return 0;
}

static int compare_password_with_hash(const char *hash, unsigned long hash_length, const char *cleartext,
				      unsigned long cleartext_length, int *is_error)
{
	(void)hash;
	(void)hash_length;
	(void)cleartext;
	(void)cleartext_length;
	*is_error = 0;
	return 1;
}

struct st_mysql_auth {
	int interface_version;
	const char *client_auth_plugin;
	int (*authenticate_user)(plugin_vio *vio, server_auth_info *info);
	int (*generate_authentication_string)(char *outbuf, unsigned int *outbuflen, const char *inbuf, unsigned int inbuflen);
	int (*validate_authentication_string)(char *const inbuf, unsigned int buflen);
	int (*set_salt)(const char *password, unsigned int password_len, unsigned char *salt, unsigned char *salt_len);
	const unsigned long authentication_flags;
	int (*compare_password_with_hash)(const char *hash, unsigned long hash_length, const char *cleartext,
					  unsigned long cleartext_length, int *is_error);
};

static struct st_mysql_auth auth = {
	0x0201, "mysql_native_password", authenticate_user, generate_authentication_string,
	validate_authentication_string, set_salt, 0, compare_password_with_hash,
};

struct st_mysql_plugin {
	int type;
	void *info;
	const char *name;
	const char *author;
	const char *descr;
	int license;
	int (*init)(void *);
	int (*check_uninstall)(void *);
	int (*deinit)(void *);
	unsigned int version;
	void *status_vars;
	void *system_vars;
	void *reserved1;
	unsigned long flags;
};

EXPORT int _mysql_plugin_interface_version_ = 0x010B;
EXPORT int _mysql_sizeof_struct_st_plugin_ = sizeof(struct st_mysql_plugin);
EXPORT struct st_mysql_plugin _mysql_plugin_declarations_[] = {
	{MYSQL_AUTHENTICATION_PLUGIN, &auth, name, author, description, PLUGIN_LICENSE_GPL, 0, 0, 0, 0x0100, 0, 0, 0, 0},
	{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
};

#endif
