/*
 Copyright 2021 - 2023 Highgo Solutions, Inc.
 Licensed under the Apache License, Version 2.0 (the "License");
 you may not use this file except in compliance with the License.
 You may obtain a copy of the License at

 http://www.apache.org/licenses/LICENSE-2.0

 Unless required by applicable law or agreed to in writing, software
 distributed under the License is distributed on an "AS IS" BASIS,
 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 See the License for the specific language governing permissions and
 limitations under the License.
*/

package pgbackrest

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/highgo/ivory-operator/internal/config"
	"github.com/highgo/ivory-operator/internal/initialize"
	ivory "github.com/highgo/ivory-operator/internal/ivory"
	"github.com/highgo/ivory-operator/internal/naming"
	"github.com/highgo/ivory-operator/pkg/apis/ivory-operator.highgo.com/v1beta1"
)

const (
	// defaultRepo1Path stores the default pgBackRest repo path
	defaultRepo1Path = "/pgbackrest/"

	// DefaultStanzaName is the name of the default pgBackRest stanza
	DefaultStanzaName = "db"

	// CMInstanceKey is the name of the pgBackRest configuration file for a IvorySQL instance
	CMInstanceKey = "pgbackrest_instance.conf"

	// CMRepoKey is the name of the pgBackRest configuration file for a pgBackRest dedicated
	// repository host
	CMRepoKey = "pgbackrest_repo.conf"

	// configDirectory is the pgBackRest configuration directory.
	configDirectory = "/etc/pgbackrest/conf.d"

	// ConfigHashKey is the name of the file storing the pgBackRest config hash
	ConfigHashKey = "config-hash"

	// repoMountPath is where to mount the pgBackRest repo volume.
	repoMountPath = "/pgbackrest"

	serverConfigAbsolutePath   = configDirectory + "/" + serverConfigProjectionPath
	serverConfigProjectionPath = "~ivory-operator_server.conf"

	serverConfigMapKey = "pgbackrest-server.conf"

	// serverMountPath is the directory containing the TLS server certificate
	// and key. This is outside of configDirectory so the hash calculated by
	// backup jobs does not change when the primary changes.
	serverMountPath = "/etc/pgbackrest/server"
)

const (
	iniGeneratedWarning = "" +
		"# Generated by ivory-operator. DO NOT EDIT.\n" +
		"# Your changes will not be saved.\n"
)

// CreatePGBackRestConfigMapIntent creates a configmap struct with pgBackRest pgbackrest.conf settings in the data field.
// The keys within the data field correspond to the use of that configuration.
// pgbackrest_job.conf is used by certain jobs, such as stanza create and backup
// pgbackrest_primary.conf is used by the primary database pod
// pgbackrest_repo.conf is used by the pgBackRest repository pod
func CreatePGBackRestConfigMapIntent(ivoryCluster *v1beta1.IvoryCluster,
	repoHostName, configHash, serviceName, serviceNamespace string,
	instanceNames []string) *corev1.ConfigMap {

	meta := naming.PGBackRestConfig(ivoryCluster)
	meta.Annotations = naming.Merge(
		ivoryCluster.Spec.Metadata.GetAnnotationsOrNil(),
		ivoryCluster.Spec.Backups.PGBackRest.Metadata.GetAnnotationsOrNil())
	meta.Labels = naming.Merge(
		ivoryCluster.Spec.Metadata.GetLabelsOrNil(),
		ivoryCluster.Spec.Backups.PGBackRest.Metadata.GetLabelsOrNil(),
		naming.PGBackRestConfigLabels(ivoryCluster.GetName()),
	)

	cm := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{
			Kind:       "ConfigMap",
			APIVersion: "v1",
		},
		ObjectMeta: meta,
	}

	// create an empty map for the config data
	initialize.StringMap(&cm.Data)

	addDedicatedHost := DedicatedRepoHostEnabled(ivoryCluster)
	pgdataDir := ivory.DataDirectory(ivoryCluster)
	// Port will always be populated, since the API will set a default of 5432 if not provided
	pgPort := *ivoryCluster.Spec.Port
	cm.Data[CMInstanceKey] = iniGeneratedWarning +
		populatePGInstanceConfigurationMap(
			serviceName, serviceNamespace, repoHostName,
			pgdataDir, pgPort, ivoryCluster.Spec.Backups.PGBackRest.Repos,
			ivoryCluster.Spec.Backups.PGBackRest.Global,
		).String()

	// As the cluster transitions from having a repository host to having none,
	// IvorySQL instances that have not rolled out expect to mount a server
	// config file. Always populate that file so those volumes stay valid and
	// Kubernetes propagates their contents to those pods.
	cm.Data[serverConfigMapKey] = ""

	if addDedicatedHost && repoHostName != "" {
		cm.Data[serverConfigMapKey] = iniGeneratedWarning +
			serverConfig(ivoryCluster).String()

		cm.Data[CMRepoKey] = iniGeneratedWarning +
			populateRepoHostConfigurationMap(
				serviceName, serviceNamespace,
				pgdataDir, pgPort, instanceNames,
				ivoryCluster.Spec.Backups.PGBackRest.Repos,
				ivoryCluster.Spec.Backups.PGBackRest.Global,
			).String()
	}

	cm.Data[ConfigHashKey] = configHash

	return cm
}

// MakePGBackrestLogDir creates the pgBackRest default log path directory used when a
// dedicated repo host is configured.
func MakePGBackrestLogDir(template *corev1.PodTemplateSpec,
	cluster *v1beta1.IvoryCluster) {

	var pgBackRestLogPath string
	for _, repo := range cluster.Spec.Backups.PGBackRest.Repos {
		if repo.Volume != nil {
			pgBackRestLogPath = fmt.Sprintf(naming.PGBackRestRepoLogPath, repo.Name)
			break
		}
	}

	container := corev1.Container{
		Command:         []string{"bash", "-c", "mkdir -p " + pgBackRestLogPath},
		Image:           config.PGBackRestContainerImage(cluster),
		ImagePullPolicy: cluster.Spec.ImagePullPolicy,
		Name:            naming.ContainerPGBackRestLogDirInit,
		SecurityContext: initialize.RestrictedSecurityContext(),
	}

	// Set the container resources to the 'pgbackrest' container configuration.
	for i, c := range template.Spec.Containers {
		if c.Name == naming.PGBackRestRepoContainerName {
			container.Resources = template.Spec.Containers[i].Resources
			break
		}
	}
	template.Spec.InitContainers = append(template.Spec.InitContainers, container)
}

// RestoreCommand returns the command for performing a pgBackRest restore.  In addition to calling
// the pgBackRest restore command with any pgBackRest options provided, the script also does the
// following:
//   - Removes the patroni.dynamic.json file if present.  This ensures the configuration from the
//     cluster being restored from is not utilized when bootstrapping a new cluster, and the
//     configuration for the new cluster is utilized instead.
//   - Starts the database and allows recovery to complete.  A temporary ivorysql.conf file
//     with the minimum settings needed to safely start the database is created and utilized.
//   - Renames the data directory as needed to bootstrap the cluster using the restored database.
//     This ensures compatibility with the "existing" bootstrap method that is included in the
//     Patroni config when bootstrapping a cluster using an existing data directory.
func RestoreCommand(pgdata string, tablespaceVolumes []*corev1.PersistentVolumeClaim, args ...string) []string {

	// After pgBackRest restores files, IvorySQL starts in recovery to finish
	// replaying WAL files. "hot_standby" is "on" (by default) so we can detect
	// when recovery has finished. In that mode, some parameters cannot be
	// smaller than they were when IvorySQL was backed up. Configure them to
	// match the values reported by "pg_controldata". Those parameters are also
	// written to WAL files and may change during recovery. When they increase,
	// IvorySQL exits and we reconfigure and restart it.
	// For PG14, when some parameters from WAL require a restart, the behavior is
	// to pause unless a restart is requested. For this edge case, we run a CASE
	// query to check
	// (a) if the instance is in recovery;
	// (b) if so, if the WAL replay is paused;
	// (c) if so, to unpause WAL replay, allowing our expected behavior to resume.
	// A note on the IvorySQL code: we cast `pg_catalog.pg_wal_replay_resume()` as text
	// because that method returns a void (which is a non-NULL but empty result). When
	// that void is cast as a string, it is an ''
	// - https://www.ivorysql.org/docs/current/hot-standby.html
	// - https://www.ivorysql.org/docs/current/app-pgcontroldata.html

	// The postmaster.pid file is removed, if it exists, before attempting a restore.
	// This allows the restore to be tried more than once without the causing an
	// error due to the presence of the file in subsequent attempts.

	// The 'pg_ctl' timeout is set to a very large value (1 year) to ensure there
	// are no timeouts when starting or stopping Ivory.

	tablespaceCmd := ""
	for _, tablespaceVolume := range tablespaceVolumes {
		tablespaceCmd = tablespaceCmd + fmt.Sprintf(
			"\ninstall --directory --mode=0700 '/tablespaces/%s/data'",
			tablespaceVolume.Labels[naming.LabelData])
	}

	restoreScript := `declare -r pgdata="$1" opts="$2"
install --directory --mode=0700 "${pgdata}"` + tablespaceCmd + `
rm -f "${pgdata}/postmaster.pid"
bash -xc "pgbackrest restore ${opts}"
rm -f "${pgdata}/patroni.dynamic.json"
export PGDATA="${pgdata}" PGHOST='/tmp'

until [ "${recovery=}" = 'f' ]; do
if [ -z "${recovery}" ]; then
control=$(pg_controldata)
read -r max_conn <<< "${control##*max_connections setting:}"
read -r max_lock <<< "${control##*max_locks_per_xact setting:}"
read -r max_ptxn <<< "${control##*max_prepared_xacts setting:}"
read -r max_work <<< "${control##*max_worker_processes setting:}"
echo > /tmp/pg_hba.restore.conf 'local all "ivorysql" peer'
cat > /tmp/ivory.restore.conf <<EOF
archive_command = 'false'
archive_mode = 'on'
hba_file = '/tmp/pg_hba.restore.conf'
max_connections = '${max_conn}'
max_locks_per_transaction = '${max_lock}'
max_prepared_transactions = '${max_ptxn}'
max_worker_processes = '${max_work}'
unix_socket_directories = '/tmp'
EOF
if [ "$(< "${pgdata}/PG_VERSION")" -ge 12 ]; then
read -r max_wals <<< "${control##*max_wal_senders setting:}"
echo >> /tmp/ivory.restore.conf "max_wal_senders = '${max_wals}'"
fi

pg_ctl start --silent --timeout=31536000 --wait --options='--config-file=/tmp/ivory.restore.conf'
fi

recovery=$(psql -Atc "SELECT CASE
  WHEN NOT pg_catalog.pg_is_in_recovery() THEN false
  WHEN NOT pg_catalog.pg_is_wal_replay_paused() THEN true
  ELSE pg_catalog.pg_wal_replay_resume()::text = ''
END recovery" && sleep 1) || true
done

pg_ctl stop --silent --wait --timeout=31536000
mv "${pgdata}" "${pgdata}_bootstrap"`

	return append([]string{"bash", "-ceu", "--", restoreScript, "-", pgdata}, args...)
}

// populatePGInstanceConfigurationMap returns options representing the pgBackRest configuration for
// a IvorySQL instance
func populatePGInstanceConfigurationMap(
	serviceName, serviceNamespace, repoHostName, pgdataDir string,
	pgPort int32, repos []v1beta1.PGBackRestRepo,
	globalConfig map[string]string,
) iniSectionSet {

	// TODO(cbandy): pass a FQDN in already.
	repoHostFQDN := repoHostName + "-0." +
		serviceName + "." + serviceNamespace + ".svc." +
		naming.KubernetesClusterDomain(context.Background())

	global := iniMultiSet{}
	stanza := iniMultiSet{}

	// pgBackRest will log to the pgData volume for commands run on the IvorySQL instance
	global.Set("log-path", naming.PGBackRestPGDataLogPath)

	for _, repo := range repos {
		global.Set(repo.Name+"-path", defaultRepo1Path+repo.Name)

		// repo volumes do not contain configuration (unlike other repo types which has actual
		// pgBackRest settings such as "bucket", "region", etc.), so only grab the name from the
		// repo if a Volume is detected, and don't attempt to get an configs
		if repo.Volume == nil {
			for option, val := range getExternalRepoConfigs(repo) {
				global.Set(option, val)
			}
		}

		// Only "volume" (i.e. PVC-based) repos should ever have a repo host configured.  This
		// means cloud-based repos (S3, GCS or Azure) should not have a repo host configured.
		if repoHostName != "" && repo.Volume != nil {
			global.Set(repo.Name+"-host", repoHostFQDN)
			global.Set(repo.Name+"-host-type", "tls")
			global.Set(repo.Name+"-host-ca-file", certAuthorityAbsolutePath)
			global.Set(repo.Name+"-host-cert-file", certClientAbsolutePath)
			global.Set(repo.Name+"-host-key-file", certClientPrivateKeyAbsolutePath)
			global.Set(repo.Name+"-host-user", "ivory")
		}
	}

	for option, val := range globalConfig {
		global.Set(option, val)
	}

	// Now add the local IVY instance to the stanza section. The local IVY host must always be
	// index 1: https://github.com/pgbackrest/pgbackrest/issues/1197#issuecomment-708381800
	stanza.Set("pg1-path", pgdataDir)
	stanza.Set("pg1-port", fmt.Sprint(pgPort))
	stanza.Set("pg1-socket-path", ivory.SocketDirectory)

	return iniSectionSet{
		"global":          global,
		DefaultStanzaName: stanza,
	}
}

// populateRepoHostConfigurationMap returns options representing the pgBackRest configuration for
// a pgBackRest dedicated repository host
func populateRepoHostConfigurationMap(
	serviceName, serviceNamespace, pgdataDir string,
	pgPort int32, pgHosts []string, repos []v1beta1.PGBackRestRepo,
	globalConfig map[string]string,
) iniSectionSet {

	global := iniMultiSet{}
	stanza := iniMultiSet{}

	var pgBackRestLogPathSet bool
	for _, repo := range repos {
		global.Set(repo.Name+"-path", defaultRepo1Path+repo.Name)

		// repo volumes do not contain configuration (unlike other repo types which has actual
		// pgBackRest settings such as "bucket", "region", etc.), so only grab the name from the
		// repo if a Volume is detected, and don't attempt to get an configs
		if repo.Volume == nil {
			for option, val := range getExternalRepoConfigs(repo) {
				global.Set(option, val)
			}
		}

		if !pgBackRestLogPathSet && repo.Volume != nil {
			// pgBackRest will log to the first configured repo volume when commands
			// are run on the pgBackRest repo host. With our previous check in
			// DedicatedRepoHostEnabled(), we've already validated that at least one
			// defined repo has a volume.
			global.Set("log-path", fmt.Sprintf(naming.PGBackRestRepoLogPath, repo.Name))
			pgBackRestLogPathSet = true
		}
	}

	for option, val := range globalConfig {
		global.Set(option, val)
	}

	// set the configs for all IVY hosts
	for i, pgHost := range pgHosts {
		// TODO(cbandy): pass a FQDN in already.
		pgHostFQDN := pgHost + "-0." +
			serviceName + "." + serviceNamespace + ".svc." +
			naming.KubernetesClusterDomain(context.Background())

		stanza.Set(fmt.Sprintf("pg%d-host", i+1), pgHostFQDN)
		stanza.Set(fmt.Sprintf("pg%d-host-type", i+1), "tls")
		stanza.Set(fmt.Sprintf("pg%d-host-ca-file", i+1), certAuthorityAbsolutePath)
		stanza.Set(fmt.Sprintf("pg%d-host-cert-file", i+1), certClientAbsolutePath)
		stanza.Set(fmt.Sprintf("pg%d-host-key-file", i+1), certClientPrivateKeyAbsolutePath)

		stanza.Set(fmt.Sprintf("pg%d-path", i+1), pgdataDir)
		stanza.Set(fmt.Sprintf("pg%d-port", i+1), fmt.Sprint(pgPort))
		stanza.Set(fmt.Sprintf("pg%d-socket-path", i+1), ivory.SocketDirectory)
	}

	return iniSectionSet{
		"global":          global,
		DefaultStanzaName: stanza,
	}
}

// getExternalRepoConfigs returns a map containing the configuration settings for an external
// pgBackRest repository as defined in the IvoryCluster spec
func getExternalRepoConfigs(repo v1beta1.PGBackRestRepo) map[string]string {

	repoConfigs := make(map[string]string)

	if repo.Azure != nil {
		repoConfigs[repo.Name+"-type"] = "azure"
		repoConfigs[repo.Name+"-azure-container"] = repo.Azure.Container
	} else if repo.GCS != nil {
		repoConfigs[repo.Name+"-type"] = "gcs"
		repoConfigs[repo.Name+"-gcs-bucket"] = repo.GCS.Bucket
	} else if repo.S3 != nil {
		repoConfigs[repo.Name+"-type"] = "s3"
		repoConfigs[repo.Name+"-s3-bucket"] = repo.S3.Bucket
		repoConfigs[repo.Name+"-s3-endpoint"] = repo.S3.Endpoint
		repoConfigs[repo.Name+"-s3-region"] = repo.S3.Region
	}

	return repoConfigs
}

// reloadCommand returns an entrypoint that convinces the pgBackRest TLS server
// to reload its options and certificate files when they change. The process
// will appear as name in `ps` and `top`.
func reloadCommand(name string) []string {
	// Use a Bash loop to periodically check the mtime of the mounted server
	// volume and configuration file. When either changes, signal pgBackRest
	// and print the observed timestamp.
	//
	// We send SIGHUP because this allows the TLS server configuration to be
	// reloaded starting in pgBackRest 2.37. We filter by parent process to ignore
	// the forked connection handlers. The server parent process is zero because
	// it is started by Kubernetes.
	// - https://github.com/pgbackrest/pgbackrest/commit/7b3ea883c7c010aafbeb14d150d073a113b703e4

	// Coreutils `sleep` uses a lot of memory, so the following opens a file
	// descriptor and uses the timeout of the builtin `read` to wait. That same
	// descriptor gets closed and reopened to use the builtin `[ -nt` to check
	// mtimes.
	// - https://unix.stackexchange.com/a/407383
	const script = `
exec {fd}<> <(:)
until read -r -t 5 -u "${fd}"; do
  if
    [ "${filename}" -nt "/proc/self/fd/${fd}" ] &&
    pkill -HUP --exact --parent=0 pgbackrest
  then
    exec {fd}>&- && exec {fd}<> <(:)
    stat --dereference --format='Loaded configuration dated %y' "${filename}"
  elif
    { [ "${directory}" -nt "/proc/self/fd/${fd}" ] ||
      [ "${authority}" -nt "/proc/self/fd/${fd}" ]
    } &&
    pkill -HUP --exact --parent=0 pgbackrest
  then
    exec {fd}>&- && exec {fd}<> <(:)
    stat --format='Loaded certificates dated %y' "${directory}"
  fi
done
`

	// Elide the above script from `ps` and `top` by wrapping it in a function
	// and calling that.
	wrapper := `monitor() {` + script + `};` +
		` export directory="$1" authority="$2" filename="$3"; export -f monitor;` +
		` exec -a "$0" bash -ceu monitor`

	return []string{"bash", "-ceu", "--", wrapper, name,
		serverMountPath, certAuthorityAbsolutePath, serverConfigAbsolutePath}
}

// serverConfig returns the options needed to run the TLS server for cluster.
func serverConfig(cluster *v1beta1.IvoryCluster) iniSectionSet {
	global := iniMultiSet{}
	server := iniMultiSet{}

	// IPv6 support is a relatively recent addition to Kubernetes, so listen on
	// the IPv4 wildcard address and trust that Pod DNS names will resolve to
	// IPv4 addresses for now.
	//
	// NOTE(cbandy): The unspecified IPv6 address, which ends up being the IPv6
	// wildcard address, did not work in all environments. In some cases, the
	// the "server-ping" command would not connect.
	// - https://tools.ietf.org/html/rfc3493#section-3.8
	//
	// TODO(cbandy): When pgBackRest provides a way to bind to all addresses,
	// use that here and configure "server-ping" to use "localhost" which
	// Kubernetes guarantees resolves to a loopback address.
	// - https://kubernetes.io/docs/concepts/cluster-administration/networking/
	// - https://releases.k8s.io/v1.18.0/pkg/kubelet/kubelet_pods.go#L327
	// - https://releases.k8s.io/v1.23.0/pkg/kubelet/kubelet_pods.go#L345
	global.Set("tls-server-address", "0.0.0.0")

	// NOTE (dsessler7): As pointed out by Chris above, there is an issue in
	// pgBackRest (#1841), where using a wildcard address to bind all addresses
	// does not work in certain IPv6 environments. Until this is fixed, we are
	// going to workaround the issue by allowing the user to add an annotation to
	// enable IPv6. We will check for that annotation here and override the
	// "tls-server-address" setting accordingly.
	if strings.EqualFold(cluster.Annotations[naming.PGBackRestIPVersion], "ipv6") {
		global.Set("tls-server-address", "::")
	}

	// The client certificate for this cluster is allowed to connect for any stanza.
	// Without the wildcard "*", the "pgbackrest info" and "pgbackrest repo-ls"
	// commands fail with "access denied" when invoked without a "--stanza" flag.
	global.Add("tls-server-auth", clientCommonName(cluster)+"=*")

	global.Set("tls-server-ca-file", certAuthorityAbsolutePath)
	global.Set("tls-server-cert-file", certServerAbsolutePath)
	global.Set("tls-server-key-file", certServerPrivateKeyAbsolutePath)

	// Send all server logs to stderr and stdout without timestamps.
	// - stderr has ERROR messages
	// - stdout has WARN, INFO, and DETAIL messages
	//
	// The "trace" level shows when a connection is accepted, but nothing about
	// the remote address or what commands it might send.
	// - https://github.com/pgbackrest/pgbackrest/blob/release/2.38/src/command/server/server.c#L158-L159
	// - https://pgbackrest.org/configuration.html#section-log
	server.Set("log-level-console", "detail")
	server.Set("log-level-stderr", "error")
	server.Set("log-level-file", "off")
	server.Set("log-timestamp", "n")

	return iniSectionSet{
		"global":        global,
		"global:server": server,
	}
}
