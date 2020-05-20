/*
 * Copyright (C) 2020, MinIO, Inc.
 *
 * This code is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License, version 3,
 * as published by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License, version 3,
 * along with this program.  If not, see <http://www.gnu.org/licenses/>
 *
 */

package statefulsets

import (
	"fmt"
	"net"
	"strconv"

	miniov1 "github.com/minio/minio-operator/pkg/apis/operator.min.io/v1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Returns the MinIO environment variables set in configuration.
// If a user specifies a secret in the spec (for MinIO credentials) we use
// that to set MINIO_ACCESS_KEY & MINIO_SECRET_KEY.
func minioEnvironmentVars(mi *miniov1.MinIOInstance) []corev1.EnvVar {
	envVars := make([]corev1.EnvVar, 0)
	// Add all the environment variables
	for _, e := range mi.Spec.Env {
		envVars = append(envVars, e)
	}
	// Add env variables from credentials secret, if no secret provided, dont use
	// env vars. MinIO server automatically creates default credentials
	if mi.HasCredsSecret() {
		var secretName string
		secretName = mi.Spec.CredsSecret.Name
		envVars = append(envVars, corev1.EnvVar{
			Name: "MINIO_ACCESS_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "accesskey",
				},
			},
		}, corev1.EnvVar{
			Name: "MINIO_SECRET_KEY",
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Key: "secretkey",
				},
			},
		})
	}
	if mi.HasKESEnabled() {
		envVars = append(envVars, corev1.EnvVar{
			Name:  "MINIO_KMS_KES_ENDPOINT",
			Value: "https://" + net.JoinHostPort(mi.KESServiceHost(), strconv.Itoa(miniov1.KESPort)),
		}, corev1.EnvVar{
			Name:  "MINIO_KMS_KES_CERT_FILE",
			Value: "/root/.minio/certs/client.crt",
		}, corev1.EnvVar{
			Name:  "MINIO_KMS_KES_KEY_FILE",
			Value: "/root/.minio/certs/client.key",
		}, corev1.EnvVar{
			Name:  "MINIO_KMS_KES_CA_PATH",
			Value: "/root/.minio/certs/CAs/server.crt",
		}, corev1.EnvVar{
			Name:  "MINIO_KMS_KES_KEY_NAME",
			Value: miniov1.KESMinIOKey,
		})
	}

	// Return environment variables
	return envVars
}

// Returns the MinIO pods metadata set in configuration.
// If a user specifies metadata in the spec we return that
// metadata.
func minioMetadata(mi *miniov1.MinIOInstance) metav1.ObjectMeta {
	meta := metav1.ObjectMeta{}
	if mi.HasMetadata() {
		meta = *mi.Spec.Metadata
	}
	// Initialize empty fields
	if meta.Labels == nil {
		meta.Labels = make(map[string]string)
	}
	if meta.Annotations == nil {
		meta.Annotations = make(map[string]string)
	}
	// Add the additional label used by StatefulSet spec
	for k, v := range mi.MinIOPodLabels() {
		meta.Labels[k] = v
	}
	// Add the Selector labels set by user
	if mi.HasSelector() {
		for k, v := range mi.Spec.Selector.MatchLabels {
			meta.Labels[k] = v
		}
	}
	return meta
}

// Builds the volume mounts for MinIO container.
func volumeMounts(mi *miniov1.MinIOInstance) []corev1.VolumeMount {
	var mounts []corev1.VolumeMount

	// This is the case where user didn't provide a zone and we deploy a EmptyDir based
	// single node single drive (FS) MinIO deployment
	name := miniov1.MinIOVolumeName
	if mi.Spec.VolumeClaimTemplate != nil {
		name = mi.Spec.VolumeClaimTemplate.Name
	}

	if mi.Spec.VolumesPerServer == 1 {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      name + strconv.Itoa(0),
			MountPath: miniov1.MinIOVolumeMountPath,
		})
	} else {
		for i := 0; i < mi.Spec.VolumesPerServer; i++ {
			mounts = append(mounts, corev1.VolumeMount{
				Name:      name + strconv.Itoa(i),
				MountPath: miniov1.MinIOVolumeMountPath + strconv.Itoa(i),
			})
		}
	}

	if mi.RequiresAutoCertSetup() || mi.RequiresExternalCertSetup() {
		mounts = append(mounts, corev1.VolumeMount{
			Name:      mi.MinIOTLSSecretName(),
			MountPath: "/root/.minio/certs",
		})
	}

	return mounts
}

// Builds the MinIO container for a MinIOInstance.
func minioServerContainer(mi *miniov1.MinIOInstance, serviceName string) corev1.Container {
	args := []string{"server"}

	if mi.Spec.Zones[0].Servers == 1 {
		// to run in standalone mode we must pass the path
		args = append(args, miniov1.MinIOVolumeMountPath)
	} else {
		// append all the MinIOInstance replica URLs
		hosts := mi.MinIOHosts()
		for _, h := range hosts {
			args = append(args, fmt.Sprintf("%s://"+h+"%s", miniov1.Scheme, mi.VolumePath()))
		}
	}

	return corev1.Container{
		Name:  miniov1.MinIOServerName,
		Image: mi.Spec.Image,
		Ports: []corev1.ContainerPort{
			{
				ContainerPort: miniov1.MinIOPort,
			},
		},
		ImagePullPolicy: miniov1.DefaultImagePullPolicy,
		VolumeMounts:    volumeMounts(mi),
		Args:            args,
		Env:             minioEnvironmentVars(mi),
		Resources:       mi.Spec.Resources,
		LivenessProbe:   mi.Spec.Liveness,
		ReadinessProbe:  mi.Spec.Readiness,
	}
}

// Builds the tolerations for a MinIOInstance.
func minioTolerations(mi *miniov1.MinIOInstance) []corev1.Toleration {
	tolerations := make([]corev1.Toleration, 0)
	// Add all the tolerations
	for _, t := range mi.Spec.Tolerations {
		tolerations = append(tolerations, t)
	}
	// Return tolerations
	return tolerations
}

// Builds the security contexts for a MinIOInstance
func minioSecurityContext(mi *miniov1.MinIOInstance) *corev1.PodSecurityContext {
	var securityContext = corev1.PodSecurityContext{}
	if mi.Spec.SecurityContext != nil {
		securityContext = *mi.Spec.SecurityContext
	}
	return &securityContext
}

func getVolumesForContainer(mi *miniov1.MinIOInstance) []corev1.Volume {
	var podVolumes = []corev1.Volume{}
	// This is the case where user didn't provide a volume claim template and we deploy a
	// EmptyDir based MinIO deployment
	if mi.Spec.VolumeClaimTemplate == nil {
		for _, z := range mi.Spec.Zones {
			podVolumes = append(podVolumes, corev1.Volume{Name: z.Name,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: ""}}})
		}
	}
	return podVolumes
}

// NewForMinIO creates a new StatefulSet for the given Cluster.
func NewForMinIO(mi *miniov1.MinIOInstance, serviceName string) *appsv1.StatefulSet {
	var secretName string

	// If a PV isn't specified just use a EmptyDir volume
	var podVolumes = getVolumesForContainer(mi)
	var replicas = mi.MinIOReplicas()

	var keyPaths = []corev1.KeyToPath{
		{Key: "public.crt", Path: "public.crt"},
		{Key: "private.key", Path: "private.key"},
		{Key: "public.crt", Path: "CAs/public.crt"},
	}

	var MinIOCertKeyPaths = []corev1.KeyToPath{
		{Key: "public.crt", Path: "client.crt"},
		{Key: "private.key", Path: "client.key"},
	}

	var KESCertKeyPaths = []corev1.KeyToPath{
		{Key: "public.crt", Path: "CAs/server.crt"},
	}

	if mi.RequiresAutoCertSetup() {
		secretName = mi.MinIOTLSSecretName()
	} else if mi.RequiresExternalCertSetup() {
		secretName = mi.Spec.ExternalCertSecret.Name
		if mi.Spec.ExternalCertSecret.Type == "kubernetes.io/tls" {
			keyPaths = []corev1.KeyToPath{
				{Key: "tls.crt", Path: "public.crt"},
				{Key: "tls.key", Path: "private.key"},
				{Key: "tls.crt", Path: "CAs/public.crt"},
			}
		} else if mi.Spec.ExternalCertSecret.Type == "cert-manager.io/v1alpha2" {
			keyPaths = []corev1.KeyToPath{
				{Key: "tls.crt", Path: "public.crt"},
				{Key: "tls.key", Path: "private.key"},
				{Key: "ca.crt", Path: "CAs/public.crt"},
			}
		}
	}

	// Add SSL volume from SSL secret to the podVolumes
	if mi.RequiresAutoCertSetup() || mi.RequiresExternalCertSetup() {
		sources := []corev1.VolumeProjection{
			{
				Secret: &corev1.SecretProjection{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: secretName,
					},
					Items: keyPaths,
				},
			},
		}
		if mi.HasKESEnabled() {
			sources = append(sources, []corev1.VolumeProjection{
				{
					Secret: &corev1.SecretProjection{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: mi.MinIOClientTLSSecretName(),
						},
						Items: MinIOCertKeyPaths,
					},
				},
				{
					Secret: &corev1.SecretProjection{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: mi.KESTLSSecretName(),
						},
						Items: KESCertKeyPaths,
					},
				},
			}...)
		}
		podVolumes = append(podVolumes, corev1.Volume{
			Name: mi.MinIOTLSSecretName(),
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					Sources: sources,
				},
			},
		})
	}

	containers := []corev1.Container{minioServerContainer(mi, serviceName)}

	ss := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: mi.Namespace,
			Name:      mi.Name,
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(mi, schema.GroupVersionKind{
					Group:   miniov1.SchemeGroupVersion.Group,
					Version: miniov1.SchemeGroupVersion.Version,
					Kind:    miniov1.MinIOCRDResourceKind,
				}),
			},
		},
		Spec: appsv1.StatefulSetSpec{
			UpdateStrategy: appsv1.StatefulSetUpdateStrategy{
				Type: miniov1.DefaultUpdateStrategy,
			},
			PodManagementPolicy: mi.Spec.PodManagementPolicy,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					miniov1.InstanceLabel: mi.MinIOStatefulSetName(),
				},
			},
			ServiceName: serviceName,
			Replicas:    &replicas,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: minioMetadata(mi),
				Spec: corev1.PodSpec{
					Containers:       containers,
					Volumes:          podVolumes,
					ImagePullSecrets: []corev1.LocalObjectReference{mi.Spec.ImagePullSecret},
					RestartPolicy:    corev1.RestartPolicyAlways,
					Affinity:         mi.Spec.Affinity,
					SchedulerName:    mi.Scheduler.Name,
					Tolerations:      minioTolerations(mi),
					SecurityContext:  minioSecurityContext(mi),
				},
			},
		},
	}

	if mi.Spec.VolumeClaimTemplate != nil {
		pvClaim := *mi.Spec.VolumeClaimTemplate
		name := pvClaim.Name
		for i := 0; i < mi.Spec.VolumesPerServer; i++ {
			pvClaim.Name = name + strconv.Itoa(i)
			ss.Spec.VolumeClaimTemplates = append(ss.Spec.VolumeClaimTemplates, pvClaim)
		}
	}
	return ss
}