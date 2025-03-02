/*
 Copyright 2021 - 2023 Crunchy Data Solutions, Inc.
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

package postgrescluster

import (
	"context"
	"fmt"
	"io"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crunchydata/postgres-operator/internal/config"
	"github.com/crunchydata/postgres-operator/internal/initialize"
	"github.com/crunchydata/postgres-operator/internal/logging"
	"github.com/crunchydata/postgres-operator/internal/naming"
	"github.com/crunchydata/postgres-operator/internal/pgmonitor"
	"github.com/crunchydata/postgres-operator/internal/postgres"
	pgpassword "github.com/crunchydata/postgres-operator/internal/postgres/password"
	"github.com/crunchydata/postgres-operator/internal/util"
	"github.com/crunchydata/postgres-operator/pkg/apis/postgres-operator.crunchydata.com/v1beta1"
)

const (
	exporterPort = int32(9187)

	// TODO: With the current implementation of the crunchy-postgres-exporter
	// it makes sense to hard-code the database. When moving away from the
	// crunchy-postgres-exporter start.sh script we should re-evaluate always
	// setting the exporter database to `postgres`.
	exporterDB = "postgres"

	// The exporter connects to all databases over loopback using a password.
	// Kubernetes guarantees localhost resolves to loopback:
	// https://kubernetes.io/docs/concepts/cluster-administration/networking/
	// https://releases.k8s.io/v1.21.0/pkg/kubelet/kubelet_pods.go#L343
	exporterHost = "localhost"
)

// If pgMonitor is enabled the pgMonitor sidecar(s) have been added to the
// instance pod. reconcilePGMonitor will update the database to
// create the necessary objects for the tool to run
func (r *Reconciler) reconcilePGMonitor(ctx context.Context,
	cluster *v1beta1.PostgresCluster, instances *observedInstances,
	monitoringSecret *corev1.Secret) error {

	err := r.reconcilePGMonitorExporter(ctx, cluster, instances, monitoringSecret)

	return err
}

// reconcilePGMonitorExporter performs setup the postgres_exporter sidecar
// - PodExec to get setup.sql file for the postgres version
// - PodExec to run the sql in the primary database
// Status.Monitoring.ExporterConfiguration is used to determine when the
// pgMonitor postgres_exporter configuration should be added/changed to
// limit how often PodExec is used
// - TODO jmckulk: kube perms comment?
func (r *Reconciler) reconcilePGMonitorExporter(ctx context.Context,
	cluster *v1beta1.PostgresCluster, instances *observedInstances,
	monitoringSecret *corev1.Secret) error {

	var (
		writableInstance *Instance
		writablePod      *corev1.Pod
		setup            string
		pgImageSHA       string
		exporterImageSHA string
	)

	// Find the PostgreSQL instance that can execute SQL that writes to every
	// database. When there is none, return early.
	writablePod, writableInstance = instances.writablePod(naming.ContainerDatabase)
	if writableInstance == nil || writablePod == nil {
		return nil
	}

	// For the writableInstance found above
	// 1) make sure the `exporter` container is running
	// 2) get and save the imageIDs for the `exporter` and `database` containers, and
	// 3) exit early if we can't get the ImageID of either of those containers.
	// We use these ImageIDs in the hash we make to see if the operator needs to rerun
	// the `EnableExporterInPostgreSQL` funcs; that way we are always running
	// that function against an updated and running pod.
	if pgmonitor.ExporterEnabled(cluster) {
		running, known := writableInstance.IsRunning(naming.ContainerPGMonitorExporter)
		if !running || !known {
			// Exporter container needs to be available to get setup.sql;
			return nil
		}

		for _, containerStatus := range writablePod.Status.ContainerStatuses {
			if containerStatus.Name == naming.ContainerPGMonitorExporter {
				exporterImageSHA = containerStatus.ImageID
			}
			if containerStatus.Name == naming.ContainerDatabase {
				pgImageSHA = containerStatus.ImageID
			}
		}

		// Could not get container imageIDs
		if exporterImageSHA == "" || pgImageSHA == "" {
			return nil
		}
	}

	// PostgreSQL is available for writes. Prepare to either add or remove
	// pgMonitor objects.

	action := func(ctx context.Context, exec postgres.Executor) error {
		return pgmonitor.EnableExporterInPostgreSQL(ctx, exec, monitoringSecret, exporterDB, setup)
	}

	if !pgmonitor.ExporterEnabled(cluster) {
		action = func(ctx context.Context, exec postgres.Executor) error {
			return pgmonitor.DisableExporterInPostgreSQL(ctx, exec)
		}
	}

	revision, err := safeHash32(func(hasher io.Writer) error {
		// Discard log message from pgmonitor package about executing SQL.
		// Nothing is being "executed" yet.
		return action(logging.NewContext(ctx, logging.Discard()), func(
			_ context.Context, stdin io.Reader, _, _ io.Writer, command ...string,
		) error {
			_, err := io.Copy(hasher, stdin)
			if err == nil {
				// Use command and image tag in hash to execute hash on image update
				_, err = fmt.Fprint(hasher, command, pgImageSHA, exporterImageSHA)
			}
			return err
		})
	})

	if err != nil {
		return err
	}

	if revision != cluster.Status.Monitoring.ExporterConfiguration {
		// The configuration is out of date and needs to be updated.
		// Include the revision hash in any log messages.
		ctx := logging.NewContext(ctx, logging.FromContext(ctx).WithValues("revision", revision))

		if pgmonitor.ExporterEnabled(cluster) {
			exec := func(_ context.Context, stdin io.Reader, stdout, stderr io.Writer, command ...string) error {
				return r.PodExec(writablePod.Namespace, writablePod.Name, naming.ContainerPGMonitorExporter, stdin, stdout, stderr, command...)
			}
			setup, _, err = pgmonitor.Executor(exec).GetExporterSetupSQL(ctx, cluster.Spec.PostgresVersion)
		}

		// Apply the necessary SQL and record its hash in cluster.Status
		if err == nil {
			err = action(ctx, func(_ context.Context, stdin io.Reader,
				stdout, stderr io.Writer, command ...string) error {
				return r.PodExec(writablePod.Namespace, writablePod.Name, naming.ContainerDatabase, stdin, stdout, stderr, command...)
			})
		}
		if err == nil {
			cluster.Status.Monitoring.ExporterConfiguration = revision
		}
	}

	return err
}

// reconcileMonitoringSecret reconciles the secret containing authentication
// for monitoring tools
func (r *Reconciler) reconcileMonitoringSecret(
	ctx context.Context,
	cluster *v1beta1.PostgresCluster) (*corev1.Secret, error) {

	existing := &corev1.Secret{ObjectMeta: naming.MonitoringUserSecret(cluster)}
	err := errors.WithStack(
		r.Client.Get(ctx, client.ObjectKeyFromObject(existing), existing))
	if client.IgnoreNotFound(err) != nil {
		return nil, err
	}

	if !pgmonitor.ExporterEnabled(cluster) {
		// TODO: Checking if the exporter is enabled to determine when monitoring
		// secret should be created. If more tools are added to the monitoring
		// suite, they could need the secret when the exporter is not enabled.
		// This check may need to be updated.
		// Exporter is disabled; delete monitoring secret if it exists.
		if err == nil {
			err = errors.WithStack(r.deleteControlled(ctx, cluster, existing))
		}
		return nil, client.IgnoreNotFound(err)
	}

	intent := &corev1.Secret{ObjectMeta: naming.MonitoringUserSecret(cluster)}
	intent.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))

	intent.Annotations = naming.Merge(
		cluster.Spec.Metadata.GetAnnotationsOrNil(),
	)
	intent.Labels = naming.Merge(
		cluster.Spec.Metadata.GetLabelsOrNil(),
		map[string]string{
			naming.LabelCluster: cluster.Name,
			naming.LabelRole:    naming.RoleMonitoring,
		})

	intent.Data = make(map[string][]byte)

	if len(existing.Data["password"]) == 0 || len(existing.Data["verifier"]) == 0 {
		password, err := util.GenerateASCIIPassword(util.DefaultGeneratedPasswordLength)
		if err != nil {
			return nil, err
		}

		// Generate the SCRAM verifier now and store alongside the plaintext
		// password so that later reconciles don't generate it repeatedly.
		// NOTE(cbandy): We don't have a function to compare a plaintext password
		// to a SCRAM verifier.
		verifier, err := pgpassword.NewSCRAMPassword(password).Build()
		if err != nil {
			return nil, err
		}
		intent.Data["password"] = []byte(password)
		intent.Data["verifier"] = []byte(verifier)
	} else {
		intent.Data["password"] = existing.Data["password"]
		intent.Data["verifier"] = existing.Data["verifier"]
	}

	err = errors.WithStack(r.setControllerReference(cluster, intent))
	if err == nil {
		err = errors.WithStack(r.apply(ctx, intent))
	}
	if err == nil {
		return intent, nil
	}

	return nil, err
}

// addPGMonitorToInstancePodSpec performs the necessary setup to add
// pgMonitor resources on a PodTemplateSpec
func addPGMonitorToInstancePodSpec(
	cluster *v1beta1.PostgresCluster,
	template *corev1.PodTemplateSpec,
	exporterWebConfig *corev1.ConfigMap) error {

	err := addPGMonitorExporterToInstancePodSpec(cluster, template, exporterWebConfig)

	return err
}

// addPGMonitorExporterToInstancePodSpec performs the necessary setup to
// add pgMonitor exporter resources to a PodTemplateSpec
// TODO jmckulk: refactor to pass around monitoring secret; Without the secret
// the exporter container cannot be created; Testing relies on ensuring the
// monitoring secret is available
func addPGMonitorExporterToInstancePodSpec(
	cluster *v1beta1.PostgresCluster,
	template *corev1.PodTemplateSpec,
	exporterWebConfig *corev1.ConfigMap) error {

	if !pgmonitor.ExporterEnabled(cluster) {
		return nil
	}

	securityContext := initialize.RestrictedSecurityContext()
	exporterContainer := corev1.Container{
		Name:            naming.ContainerPGMonitorExporter,
		Image:           config.PGExporterContainerImage(cluster),
		ImagePullPolicy: cluster.Spec.ImagePullPolicy,
		Resources:       cluster.Spec.Monitoring.PGMonitor.Exporter.Resources,
		Command: []string{
			"/opt/cpm/bin/start.sh",
		},
		Env: []corev1.EnvVar{
			{Name: "CONFIG_DIR", Value: "/opt/cpm/conf"},
			{Name: "POSTGRES_EXPORTER_PORT", Value: fmt.Sprint(exporterPort)},
			{Name: "PGBACKREST_INFO_THROTTLE_MINUTES", Value: "10"},
			{Name: "PG_STAT_STATEMENTS_LIMIT", Value: "20"},
			{Name: "PG_STAT_STATEMENTS_THROTTLE_MINUTES", Value: "-1"},
			{Name: "EXPORTER_PG_HOST", Value: exporterHost},
			{Name: "EXPORTER_PG_PORT", Value: fmt.Sprint(*cluster.Spec.Port)},
			{Name: "EXPORTER_PG_DATABASE", Value: exporterDB},
			{Name: "EXPORTER_PG_USER", Value: pgmonitor.MonitoringUser},
			{Name: "EXPORTER_PG_PASSWORD", ValueFrom: &corev1.EnvVarSource{
				// Environment variables are not updated after a secret update.
				// This could lead to a state where the exporter does not have
				// the correct password and the container needs to restart.
				// https://kubernetes.io/docs/concepts/configuration/secret/#environment-variables-are-not-updated-after-a-secret-update
				// https://github.com/kubernetes/kubernetes/issues/29761
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: naming.MonitoringUserSecret(cluster).Name,
					},
					Key: "password",
				},
			}},
		},
		SecurityContext: securityContext,
		// ContainerPort is needed to support proper target discovery by Prometheus for pgMonitor
		// integration
		Ports: []corev1.ContainerPort{{
			ContainerPort: exporterPort,
			Name:          naming.PortExporter,
			Protocol:      corev1.ProtocolTCP,
		}},
		VolumeMounts: []corev1.VolumeMount{{
			Name: "exporter-config",
			// this is the path for custom config as defined in the start.sh script for the exporter container
			MountPath: "/conf",
		}},
	}

	template.Spec.Containers = append(template.Spec.Containers, exporterContainer)

	// add custom exporter config volume
	configVolume := corev1.Volume{
		Name: "exporter-config",
		VolumeSource: corev1.VolumeSource{
			Projected: &corev1.ProjectedVolumeSource{
				Sources: cluster.Spec.Monitoring.PGMonitor.Exporter.Configuration,
			},
		},
	}
	template.Spec.Volumes = append(template.Spec.Volumes, configVolume)

	if cluster.Spec.Monitoring.PGMonitor.Exporter.CustomTLSSecret != nil {
		configureExporterTLS(cluster, template, exporterWebConfig)
	}

	// add the proper label to support Pod discovery by Prometheus per pgMonitor configuration
	initialize.Labels(template)
	template.Labels[naming.LabelPGMonitorDiscovery] = "true"

	return nil
}

// getExporterCertSecret retrieves the custom tls cert secret projection from the exporter spec
// TODO (jmckulk): One day we might want to generate certs here
func getExporterCertSecret(cluster *v1beta1.PostgresCluster) *corev1.SecretProjection {
	if cluster.Spec.Monitoring.PGMonitor.Exporter.CustomTLSSecret != nil {
		return cluster.Spec.Monitoring.PGMonitor.Exporter.CustomTLSSecret
	}

	return nil
}

// configureExporterTLS takes a cluster and pod template spec. If enabled, the pod template spec
// will be updated with exporter tls configuration
func configureExporterTLS(cluster *v1beta1.PostgresCluster, template *corev1.PodTemplateSpec, exporterWebConfig *corev1.ConfigMap) {
	var found bool
	var exporterContainer *corev1.Container
	for i, container := range template.Spec.Containers {
		if container.Name == naming.ContainerPGMonitorExporter {
			exporterContainer = &template.Spec.Containers[i]
			found = true
		}
	}

	if found &&
		pgmonitor.ExporterEnabled(cluster) &&
		(cluster.Spec.Monitoring.PGMonitor.Exporter.CustomTLSSecret != nil) {
		// TODO (jmckulk): params for paths and such
		certVolume := corev1.Volume{Name: "exporter-certs"}
		certVolume.Projected = &corev1.ProjectedVolumeSource{
			Sources: append([]corev1.VolumeProjection{},
				corev1.VolumeProjection{
					Secret: getExporterCertSecret(cluster),
				},
			),
		}

		webConfigVolume := corev1.Volume{Name: "web-config"}
		webConfigVolume.ConfigMap = &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{
				Name: exporterWebConfig.Name,
			},
		}
		template.Spec.Volumes = append(template.Spec.Volumes, certVolume, webConfigVolume)

		mounts := []corev1.VolumeMount{{
			Name:      "exporter-certs",
			MountPath: "/certs",
		}, {
			Name:      "web-config",
			MountPath: "/web-config",
		}}

		exporterContainer.VolumeMounts = append(exporterContainer.VolumeMounts, mounts...)
		exporterContainer.Env = append(exporterContainer.Env, corev1.EnvVar{
			// TODO (jmckulk): define path not dir
			Name:  "WEB_CONFIG_DIR",
			Value: "web-config/",
		})
	}
}

// reconcileExporterWebConfig reconciles the configmap containing the webconfig for exporter tls
func (r *Reconciler) reconcileExporterWebConfig(ctx context.Context,
	cluster *v1beta1.PostgresCluster) (*corev1.ConfigMap, error) {

	existing := &corev1.ConfigMap{ObjectMeta: naming.ExporterWebConfigMap(cluster)}
	err := errors.WithStack(r.Client.Get(ctx, client.ObjectKeyFromObject(existing), existing))
	if client.IgnoreNotFound(err) != nil {
		return nil, err
	}

	if !pgmonitor.ExporterEnabled(cluster) || cluster.Spec.Monitoring.PGMonitor.Exporter.CustomTLSSecret == nil {
		// We could still have a NotFound error here so check the err.
		// If no error that means the configmap is found and needs to be deleted
		if err == nil {
			err = errors.WithStack(r.deleteControlled(ctx, cluster, existing))
		}
		return nil, client.IgnoreNotFound(err)
	}

	intent := &corev1.ConfigMap{
		ObjectMeta: naming.ExporterWebConfigMap(cluster),
		Data: map[string]string{
			"web-config.yml": `
# Generated by postgres-operator. DO NOT EDIT.
# Your changes will not be saved.


# A certificate and a key file are needed to enable TLS.
tls_server_config:
  cert_file: /certs/tls.crt
  key_file: /certs/tls.key`,
		},
	}

	intent.Annotations = naming.Merge(
		cluster.Spec.Metadata.GetAnnotationsOrNil(),
	)
	intent.Labels = naming.Merge(
		cluster.Spec.Metadata.GetLabelsOrNil(),
		map[string]string{
			naming.LabelCluster: cluster.Name,
			naming.LabelRole:    naming.RoleMonitoring,
		})

	intent.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("ConfigMap"))

	err = errors.WithStack(r.setControllerReference(cluster, intent))
	if err == nil {
		err = errors.WithStack(r.apply(ctx, intent))
	}
	if err == nil {
		return intent, nil
	}

	return nil, err
}
