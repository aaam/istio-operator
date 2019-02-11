package config

import (
	"fmt"

	istiov1beta1 "github.com/banzaicloud/istio-operator/pkg/apis/operator/v1beta1"
	"github.com/banzaicloud/istio-operator/pkg/k8sutil"
	"github.com/banzaicloud/istio-operator/pkg/util"
	"github.com/ghodss/yaml"
	"github.com/go-logr/logr"
	"github.com/goph/emperror"
	admissionv1beta1 "k8s.io/api/admissionregistration/v1beta1"
	appsv1 "k8s.io/api/apps/v1"
	apiv1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *ReconcileConfig) ReconcileGalley(log logr.Logger, istio *istiov1beta1.Config) error {

	galleyResources := make(map[string]runtime.Object)

	galleySa := &apiv1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "istio-galley-service-account",
			Namespace: istio.Namespace,
			Labels: map[string]string{
				"app": "istio-galley",
			},
		},
	}
	controllerutil.SetControllerReference(istio, galleySa, r.scheme)
	galleyResources[galleySa.Name] = galleySa

	galleyCr := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "istio-galley-cluster-role",
			Labels: map[string]string{
				"app": "istio-galley",
			},
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"admissionregistration.k8s.io"},
				Resources: []string{"validatingwebhookconfigurations"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{"config.istio.io"},
				Resources: []string{"*"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups:     []string{"*"},
				Resources:     []string{"deployments"},
				ResourceNames: []string{"istio-galley"},
				Verbs:         []string{"get"},
			},
			{
				APIGroups:     []string{"*"},
				Resources:     []string{"endpoints"},
				ResourceNames: []string{"istio-galley"},
				Verbs:         []string{"get"},
			},
		},
	}
	controllerutil.SetControllerReference(istio, galleyCr, r.scheme)
	galleyResources[galleyCr.Name] = galleyCr

	galleyCrb := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "istio-galley-admin-role-binding",
			Labels: map[string]string{
				"app": "istio-galley",
			},
		},
		RoleRef: rbacv1.RoleRef{
			Kind:     "ClusterRole",
			APIGroup: "rbac.authorization.k8s.io",
			Name:     galleyCr.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      galleySa.Name,
				Namespace: istio.Namespace,
			},
		},
	}
	controllerutil.SetControllerReference(istio, galleyCrb, r.scheme)
	galleyResources[galleyCrb.Name] = galleyCrb

	webhookConfig, err := validatingWebhookConfig(istio.Namespace)
	if err != nil {
		return emperror.With(err)
	}
	galleyCm := &apiv1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "istio-galley-configuration",
			Namespace: istio.Namespace,
			Labels: map[string]string{
				"app":   "istio-galley",
				"istio": "mixer",
			},
		},
		Data: map[string]string{
			"validatingwebhookconfiguration.yaml": webhookConfig,
		},
	}
	controllerutil.SetControllerReference(istio, galleyCm, r.scheme)
	galleyResources[galleyCm.Name] = galleyCm

	galleyDeploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "istio-galley-deployment",
			Namespace: istio.Namespace,
			Labels: map[string]string{
				"app":   "istio-galley",
				"istio": "galley",
			},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: util.IntPointer(1),
			Strategy: appsv1.DeploymentStrategy{
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxSurge:       util.IntstrPointer(1),
					MaxUnavailable: util.IntstrPointer(0),
				},
			},
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"istio": "galley",
				},
			},
			Template: apiv1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"istio": "galley",
					},
					Annotations: defaultDeployAnnotations(),
				},
				Spec: apiv1.PodSpec{
					ServiceAccountName: galleySa.Name,
					Containers: []apiv1.Container{
						{
							Name:            "validator",
							Image:           "docker.io/istio/galley:1.0.5",
							ImagePullPolicy: apiv1.PullIfNotPresent,
							Ports: []apiv1.ContainerPort{
								{
									ContainerPort: 443,
								},
								{
									ContainerPort: 9093,
								},
							},
							Command: []string{
								"/usr/local/bin/galley",
								"validator",
								fmt.Sprintf("--deployment-namespace=%s", istio.Namespace),
								"--caCertFile=/etc/istio/certs/root-cert.pem",
								"--tlsCertFile=/etc/istio/certs/cert-chain.pem",
								"--tlsKeyFile=/etc/istio/certs/key.pem",
								"--healthCheckInterval=1s",
								"--healthCheckFile=/health",
								"--webhook-config-file",
								"/etc/istio/config/validatingwebhookconfiguration.yaml",
							},
							VolumeMounts: []apiv1.VolumeMount{
								{
									Name:      "certs",
									MountPath: "/etc/istio/certs",
									ReadOnly:  true,
								},
								{
									Name:      "config",
									MountPath: "/etc/istio/config",
									ReadOnly:  true,
								},
							},
							LivenessProbe:  galleyProbe(),
							ReadinessProbe: galleyProbe(),
							Resources:      defaultResources(),
						},
					},
					Volumes: []apiv1.Volume{
						{
							Name: "certs",
							VolumeSource: apiv1.VolumeSource{
								Secret: &apiv1.SecretVolumeSource{
									SecretName: fmt.Sprintf("istio.%s", galleySa.Name),
								},
							},
						},
						{
							Name: "config",
							VolumeSource: apiv1.VolumeSource{
								ConfigMap: &apiv1.ConfigMapVolumeSource{
									LocalObjectReference: apiv1.LocalObjectReference{
										Name: "istio-galley-configuration",
									},
								},
							},
						},
					},
					Affinity: &apiv1.Affinity{},
				},
			},
		},
	}
	controllerutil.SetControllerReference(istio, galleyDeploy, r.scheme)
	galleyResources[galleyDeploy.Name] = galleyDeploy

	galleySvc := &apiv1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "istio-galley",
			Namespace: istio.Namespace,
			Labels: map[string]string{
				"istio": "galley",
			},
		},
		Spec: apiv1.ServiceSpec{
			Ports: []apiv1.ServicePort{
				{
					Name: "https-validation",
					Port: 443,
				},
				{
					Name: "https-monitoring",
					Port: 9093,
				},
			},
			Selector: map[string]string{
				"istio": "galley",
			},
		},
	}
	controllerutil.SetControllerReference(istio, galleySvc, r.scheme)
	galleyResources[galleySvc.Name] = galleySvc

	for name, res := range galleyResources {
		err := k8sutil.ReconcileResource(log, r.Client, istio.Namespace, name, res)
		if err != nil {
			return emperror.WrapWith(err, "failed to reconcile resource", "resource", res.GetObjectKind().GroupVersionKind().Kind, "name", name)
		}
	}

	//TODO: wait until galley deployment is available
	//while true; do
	///kubectl -n {{ .Release.Namespace }} get deployment istio-galley 2>/dev/null
	//if [ "$?" -eq 0 ]; then
	//break
	//fi
	//sleep 1
	//done
	///kubectl -n {{ .Release.Namespace }} rollout status deployment istio-galley
	//if [ "$?" -ne 0 ]; then
	//echo "istio-galley deployment rollout status check failed"
	//exit 1
	//fi
	//echo "istio-galley deployment ready for configuration validation"

	return nil
}

func validatingWebhookConfig(ns string) (string, error) {
	fail := admissionv1beta1.Fail
	webhook := admissionv1beta1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "istio-galley",
			Namespace: ns,
			Labels: map[string]string{
				"app": "istio-galley",
			},
		},
		Webhooks: []admissionv1beta1.Webhook{
			{
				Name: "pilot.validation.istio.io",
				ClientConfig: admissionv1beta1.WebhookClientConfig{
					Service: &admissionv1beta1.ServiceReference{
						Name:      "istio-galley",
						Namespace: ns,
						Path:      util.StrPointer("/admitpilot"),
					},
					CABundle: []byte{},
				},
				Rules: []admissionv1beta1.RuleWithOperations{
					{
						Operations: []admissionv1beta1.OperationType{
							admissionv1beta1.Create,
							admissionv1beta1.Update,
						},
						Rule: admissionv1beta1.Rule{
							APIGroups:   []string{"config.istio.io"},
							APIVersions: []string{"v1alpha2"},
							Resources:   []string{"httpapispecs", "httpapispecbindings", "quotaspecs", "quotaspecbindings"},
						},
					},
					{
						Operations: []admissionv1beta1.OperationType{
							admissionv1beta1.Create,
							admissionv1beta1.Update,
						},
						Rule: admissionv1beta1.Rule{
							APIGroups:   []string{"rbac.istio.io"},
							APIVersions: []string{"*"},
							Resources:   []string{"*"},
						},
					},
					{
						Operations: []admissionv1beta1.OperationType{
							admissionv1beta1.Create,
							admissionv1beta1.Update,
						},
						Rule: admissionv1beta1.Rule{
							APIGroups:   []string{"authentication.istio.io"},
							APIVersions: []string{"*"},
							Resources:   []string{"*"},
						},
					},
					{
						Operations: []admissionv1beta1.OperationType{
							admissionv1beta1.Create,
							admissionv1beta1.Update,
						},
						Rule: admissionv1beta1.Rule{
							APIGroups:   []string{"networking.istio.io"},
							APIVersions: []string{"*"},
							Resources:   []string{"destinationrules", "envoyfilters", "gateways", "serviceentries", "virtualservices"},
						},
					},
				},
				FailurePolicy: &fail,
			},
			{
				Name: "mixer.validation.istio.io",
				ClientConfig: admissionv1beta1.WebhookClientConfig{
					Service: &admissionv1beta1.ServiceReference{
						Name:      "istio-galley",
						Namespace: ns,
						Path:      util.StrPointer("/admitmixer"),
					},
					CABundle: []byte{},
				},
				Rules: []admissionv1beta1.RuleWithOperations{
					{
						Operations: []admissionv1beta1.OperationType{
							admissionv1beta1.Create,
							admissionv1beta1.Update,
						},
						Rule: admissionv1beta1.Rule{
							APIGroups:   []string{"config.istio.io"},
							APIVersions: []string{"v1alpha2"},
							Resources: []string{
								"rules",
								"attributemanifests",
								"circonuses",
								"deniers",
								"fluentds",
								"kubernetesenvs",
								"listcheckers",
								"memquotas",
								"noops",
								"opas",
								"prometheuses",
								"rbacs",
								"servicecontrols",
								"solarwindses",
								"stackdrivers",
								"statsds",
								"stdios",
								"apikeys",
								"authorizations",
								"checknothings",
								"listentries",
								"logentries",
								"metrics",
								"quotas",
								"reportnothings",
								"servicecontrolreports",
								"tracespans"},
						},
					},
				},
				FailurePolicy: &fail,
			},
		},
	}
	marshaledConfig, err := yaml.Marshal(webhook)
	if err != nil {
		return "", emperror.Wrap(err, "failed to marshal webhook config")
	}
	return string(marshaledConfig), nil
}

func galleyProbe() *apiv1.Probe {
	return &apiv1.Probe{
		Handler: apiv1.Handler{
			Exec: &apiv1.ExecAction{
				Command: []string{
					"/usr/local/bin/galley",
					"probe",
					"--probe-path=/health",
					"--interval=10s",
				},
			},
		},
		InitialDelaySeconds: 5,
		PeriodSeconds:       5,
	}
}
