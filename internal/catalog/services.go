package catalog

import (
	"net"
	"strconv"
)

// The API port of each service, in its container and, before any offset,
// on the host.
const (
	identityPort    = 9030
	accountsPort    = 9020
	objectStorePort = 9000
	secretStorePort = 9010
	mailboxPort     = 9040
	queuesPort      = 9050
	functionsPort   = 9060
	consolePort     = 9070
)

// apiPorts maps each service to its API port, for the URLs it is wired to.
var apiPorts = map[string]int{
	Identity:    identityPort,
	Accounts:    accountsPort,
	ObjectStore: objectStorePort,
	SecretStore: secretStorePort,
	Mailbox:     mailboxPort,
	Queues:      queuesPort,
	Functions:   functionsPort,
	Console:     consolePort,
}

// services is the catalog. Every image, version, port, mount and variable
// here is the one console's compose.yaml configures, and each variable is in
// the service's README settings table. Versions are the latest release of
// each service.
var services = []*Service{
	{
		Name:    Identity,
		AWS:     "IAM, STS",
		Image:   Registry + "/identity",
		Version: "0.5.2",
		Ports:   []Port{{Name: PortAPI, Default: identityPort}},
		Volume:  "identitydata",
		// identity serves IAM and STS at the same URL.
		EndpointVars: []string{"IAM", "STS"},
		wire: func(w Wiring) map[string]string {
			env := rootKeyEnv(w)
			setIfSelected(env, w, "TULE_Identity_AccountsEndpoint", Accounts)
			return env
		},
	},
	{
		Name:         Accounts,
		AWS:          "Organizations",
		Image:        Registry + "/accounts",
		Version:      "0.5.1",
		Ports:        []Port{{Name: PortAPI, Default: accountsPort}},
		Volume:       "accountsdata",
		EndpointVars: []string{"ORGANIZATIONS"},
		wire:         serviceEnv,
	},
	{
		Name:         ObjectStore,
		AWS:          "S3",
		Image:        Registry + "/object-store",
		Version:      "0.13.1",
		Ports:        []Port{{Name: PortAPI, Default: objectStorePort}},
		Volume:       "objectdata",
		EndpointVars: []string{"S3"},
		wire: func(w Wiring) map[string]string {
			env := serviceEnv(w)
			// Bucket notifications are delivered to queues and functions.
			setIfSelected(env, w, "TULE_Storage_QueuesEndpoint", Queues)
			setIfSelected(env, w, "TULE_Storage_FunctionsEndpoint", Functions)
			return env
		},
	},
	{
		Name:         SecretStore,
		AWS:          "Secrets Manager",
		Image:        Registry + "/secret-store",
		Version:      "0.6.1",
		Ports:        []Port{{Name: PortAPI, Default: secretStorePort}},
		Volume:       "secretdata",
		EndpointVars: []string{"SECRETS_MANAGER"},
		wire: func(w Wiring) map[string]string {
			env := serviceEnv(w)
			// Rotation invokes rotation functions there.
			setIfSelected(env, w, "TULE_Store_FunctionsEndpoint", Functions)
			return env
		},
	},
	{
		Name:    Mailbox,
		AWS:     "SES",
		Image:   Registry + "/mailbox",
		Version: "0.4.1",
		Ports:   []Port{{Name: PortAPI, Default: mailboxPort}},
		Volume:  "maildata",
		// mailbox serves the SES v2 API only. An SES v1 client pointed at
		// it fails there rather than calling AWS.
		EndpointVars: []string{"SES", "SESV2"},
		wire:         serviceEnv,
	},
	{
		Name:         Queues,
		AWS:          "SQS",
		Image:        Registry + "/queues",
		Version:      "0.6.1",
		Ports:        []Port{{Name: PortAPI, Default: queuesPort}},
		Volume:       "queuesdata",
		EndpointVars: []string{"SQS"},
		wire: func(w Wiring) map[string]string {
			env := serviceEnv(w)
			// Queue URLs name the host clients on this machine reach
			// queues at.
			env["TULE_Queues_BaseURL"] = "http://" + net.JoinHostPort(
				w.PublicHost, strconv.Itoa(w.HostPorts.Of(Queues, PortAPI)))
			return env
		},
	},
	{
		Name:    Functions,
		AWS:     "Lambda",
		Image:   Registry + "/functions",
		Version: "0.7.0",
		// Only the Lambda API is published: the function containers reach
		// the Runtime API on the stack network.
		Ports:        []Port{{Name: PortAPI, Default: functionsPort}},
		Volume:       "functionsdata",
		EndpointVars: []string{"LAMBDA"},
		DependsOn:    []string{Queues},
		OptIn:        true,
		DockerSocket: true,
		Notes:        "opt-in: mounts the Docker socket, which is root-equivalent on the host",
		wire: func(w Wiring) map[string]string {
			env := serviceEnv(w)
			setIfSelected(env, w, "TULE_Functions_QueuesEndpoint", Queues)
			functionsNetworkEnv(env, w.Network)
			return env
		},
	},
	{
		Name:    Console,
		Image:   Registry + "/console",
		Version: "0.3.11",
		Ports:   []Port{{Name: PortAPI, Default: consolePort}},
		DependsOn: []string{
			Identity, Accounts, ObjectStore, SecretStore, Mailbox, Queues,
		},
		Notes: "web console for the other services; needs at least one of them",
		// The console takes no root key: it signs in with a key a person
		// gives it.
		wire: func(w Wiring) map[string]string {
			env := map[string]string{}
			setIfSelected(env, w, "TULE_Console_IdentityEndpoint", Identity)
			setIfSelected(env, w, "TULE_Console_AccountsEndpoint", Accounts)
			setIfSelected(env, w, "TULE_Console_ObjectStoreEndpoint", ObjectStore)
			setIfSelected(env, w, "TULE_Console_SecretStoreEndpoint", SecretStore)
			setIfSelected(env, w, "TULE_Console_MailboxEndpoint", Mailbox)
			setIfSelected(env, w, "TULE_Console_QueuesEndpoint", Queues)
			setIfSelected(env, w, "TULE_Console_FunctionsEndpoint", Functions)
			return env
		},
	},
}

// functionsNetworkEnv attaches the function containers to the stack
// network, where they reach the Runtime API at functions:9061, its default
// port, and the other services by their names. functions refuses a
// RuntimeAPIHost other than host.docker.internal without a RuntimeAPIBind;
// the image sets 0.0.0.0, and it is set here too so the stack does not lean
// on the image's default.
func functionsNetworkEnv(env map[string]string, network string) {
	env["TULE_Functions_ContainerNetwork"] = network
	env["TULE_Functions_RuntimeAPIHost"] = Functions
	env["TULE_Functions_RuntimeAPIBind"] = "0.0.0.0"
}

// rootKeyEnv is the root key every service but the console is configured
// with.
func rootKeyEnv(w Wiring) map[string]string {
	return map[string]string{
		"TULE_Auth_AccessKeyID":     w.RootKey.ID,
		"TULE_Auth_SecretAccessKey": w.RootKey.Secret,
	}
}

// serviceEnv is the root key and, with identity selected, identity's
// endpoint, so the keys identity issues are accepted too.
func serviceEnv(w Wiring) map[string]string {
	env := rootKeyEnv(w)
	setIfSelected(env, w, "TULE_Auth_IdentityEndpoint", Identity)
	return env
}

// setIfSelected sets key to the URL of the service named target when it is
// selected.
func setIfSelected(env map[string]string, w Wiring, key, target string) {
	if !w.Selection.Has(target) {
		return
	}
	env[key] = serviceURL(target)
}

// serviceURL is the base URL of the service named name on the stack network.
func serviceURL(name string) string {
	return "http://" + net.JoinHostPort(name, strconv.Itoa(apiPorts[name]))
}
