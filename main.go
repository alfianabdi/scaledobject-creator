package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	kedav1alpha1 "github.com/kedacore/keda/v2/apis/keda/v1alpha1"
	yaml "gopkg.in/yaml.v2"

	kedaclientset "github.com/kedacore/keda/v2/pkg/generated/clientset/versioned"
	flag "github.com/spf13/pflag"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
        "k8s.io/client-go/plugin/pkg/client/auth"
)

type ScalingConfig struct {
	StartTime       string `json:"start,omitempty" yaml:"start,omitempty"`
	StopTime        string `json:"stop,omitempty" yaml:"stop,omitempty"`
	DesiredReplicas int32  `json:"desired,omitempty" yaml:"desired,omitempty"`
	MinimumReplicas int32  `json:"min,omitempty" yaml:"min,omitempty"`
	MaximumReplicas int32  `json:"max,omitempty" yaml:"max,omitempty"`
}

type NamespaceScalingConfig struct {
	Default      *ScalingConfig            `json:"default,omitempty" yaml:"default,omitempty"`
	Deployments  map[string]*ScalingConfig `json:"deployments,omitempty" yaml:"deployments,omitempty"`
	StatefulSets map[string]*ScalingConfig `json:"statefulsets,omitempty" yaml:"statefulsets,omitempty"`
}

type APIObject struct {
	ConfigMap        *corev1.ConfigMap
	DeploymentList   *appsv1.DeploymentList
	ScaledObjectList *kedav1alpha1.ScaledObjectList
}

type patchStringValue struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

type patchInt32Value struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value int32  `json:"value"`
}

func main() {
	defaultKubeconfig := filepath.Join(homedir.HomeDir(), ".kube", "config")

	var incluster *bool = flag.Bool("incluster", true, "Use in cluster authentication")
	var kubeconfig *string = flag.String("kubeconfig", defaultKubeconfig, "The absolute path to the Kubeconfig")
	var excludedNamespaces = flag.StringSlice("excluded-namespace", []string{}, "The namespaces to be excluded")

	flag.Parse()

	// Create a map for excluded namespace
	excludedNamespaceMap := make(map[string]string)
	for _, ns := range *excludedNamespaces {
		excludedNamespaceMap[ns] = ns
	}

	var config *rest.Config
	var err error
	if *incluster {
		config, err = rest.InClusterConfig()
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	}
	// Client Set for Kubernetes Core and Keda CRs

	if err != nil {
		panic(err.Error())
	}
	clientSet, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	kedaClientSet, err := kedaclientset.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	// List namespaces
	namespaceList, err := clientSet.CoreV1().Namespaces().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	// Iterate all namespaces
	for _, namespace := range namespaceList.Items {
		_, exist := excludedNamespaceMap[namespace.Name]
		if !exist {
			var nsSc NamespaceScalingConfig
			// Get configmap, list scaled objects and deployments
			fmt.Println("Current namespace :", namespace.Name)

			var wg sync.WaitGroup
			wg.Add(3)
			var configMap *corev1.ConfigMap
			var deploymentList *appsv1.DeploymentList
			var scaledObjectList *kedav1alpha1.ScaledObjectList

			go func() {
				configMap, err = clientSet.CoreV1().ConfigMaps(namespace.Name).Get(context.TODO(), "scaling-configuration", metav1.GetOptions{})
				wg.Done()
			}()

			go func() {
				deploymentList, err = clientSet.AppsV1().Deployments(namespace.Name).List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					panic(err.Error())
				}
				wg.Done()
			}()

			go func() {
				scaledObjectList, err = kedaClientSet.KedaV1alpha1().ScaledObjects(namespace.Name).List(context.TODO(), metav1.ListOptions{})
				if err != nil {
					panic(err.Error())
				}
				wg.Done()
			}()

			wg.Wait()

			err = yaml.Unmarshal([]byte(configMap.Data["config"]), &nsSc)
			if err != nil {
				panic(err.Error())
			}
			// fmt.Println(deploymentList)
			for _, deployment := range deploymentList.Items {
				scaledObject := getScaledObjectForDeployment(&deployment, scaledObjectList)
				scalingConfig := getScalingConfigForDeployment(&deployment, &nsSc)
				if scaledObject == nil {
					fmt.Println("Create scaledobject for deployment ", deployment.Name)
					scalingObject := createScaledObject(namespace.Name, deployment.Name, &scalingConfig)
					_, err = kedaClientSet.KedaV1alpha1().ScaledObjects(namespace.Name).Create(context.TODO(), &scalingObject, metav1.CreateOptions{})
					if err != nil {
						fmt.Println(err)
					}
				} else {
					currentScalingConfig := getScalingConfigFromScaledObject(scaledObject)
					scaledObjectPatches := createScalingConfigPatch(scalingConfig, currentScalingConfig)
					if len(scaledObjectPatches) > 0 {
						scaledObjectPatchesData, _ := json.Marshal(scaledObjectPatches)
						fmt.Println(string(scaledObjectPatchesData))
						_, err = kedaClientSet.KedaV1alpha1().ScaledObjects(namespace.Name).Patch(context.TODO(), scaledObject.Name, types.JSONPatchType, scaledObjectPatchesData, metav1.PatchOptions{})
						if err != nil {
							fmt.Println("Failed to patch scaled object ", err)
						}
					}
				}
			}

			// noRefScaledObjects := getScaledObjectsWithNoReference(scaledObjectList)
			// for _, noRefScaledObject := range noRefScaledObjects {
			// 	fmt.Printf("Scaled object %s in namespace %s has no reference\n", noRefScaledObject.Name, noRefScaledObject.Namespace)
			// 	err := kedaClientSet.KedaV1alpha1().ScaledObjects(namespace.Name).Delete(context.TODO(), noRefScaledObject.Name, metav1.DeleteOptions{})
			// 	if err != nil {
			// 		fmt.Println(err)
			// 	}
			// }
		}
	}
}

func getScaledObjectForDeployment(deployment *appsv1.Deployment, scaledObjectList *kedav1alpha1.ScaledObjectList) *kedav1alpha1.ScaledObject {
	deploymentName := deployment.Name
	if len(scaledObjectList.Items) == 0 {
		return nil
	}

	for _, scaledObject := range scaledObjectList.Items {
		scaledObjectName := scaledObject.Name
		if deploymentName == scaledObjectName {
			return &scaledObject
		}
	}
	return nil
}

// func getScaledObjectsWithNoReference(scaledObjectList *kedav1alpha1.ScaledObjectList) []kedav1alpha1.ScaledObject {
// 	scaledObjects := make([]kedav1alpha1.ScaledObject, 0)
// 	for _, scaledObject := range scaledObjectList.Items {
// 		if len(scaledObject.Status.Health) == 0 {
// 			scaledObjects = append(scaledObjects, scaledObject)
// 		}
// 	}
// 	return scaledObjects
// }

func getScalingConfigForDeployment(deployment *appsv1.Deployment, config *NamespaceScalingConfig) ScalingConfig {
	defaultScalingConfig := ScalingConfig{
		StartTime: "0 9 * * 1,2,3,4,5",
		// StartTime:       "00 10 * * *",
		StopTime:        "0 20 * * 1,2,3,4,5",
		DesiredReplicas: 1,
		MinimumReplicas: 0,
		MaximumReplicas: 1,
	}
	if config.Default == nil {
		return defaultScalingConfig
	}
	if config.Deployments[deployment.Name] != nil {
		return *config.Deployments[deployment.Name]
	}
	return *config.Default
}

func getScalingConfigFromScaledObject(scaledObject *kedav1alpha1.ScaledObject) ScalingConfig {
	desiredReplicas := int32(1)
	maximumReplicas := scaledObject.Spec.MaxReplicaCount
	minimumReplicas := scaledObject.Spec.MinReplicaCount
	startTime := "00 10 * * 1,2,3,4,5"
	stopTime := "00 20 * * 1,2,3,4,5"

	for _, trigger := range scaledObject.Spec.Triggers {
		if trigger.Type == "cron" {
			desiredReplicasInt, _ := strconv.ParseInt(trigger.Metadata["desiredReplicas"], 10, 32)
			desiredReplicas = int32(desiredReplicasInt)
			startTime = trigger.Metadata["start"]
			stopTime = trigger.Metadata["end"]
		}
	}
	return ScalingConfig{
		DesiredReplicas: desiredReplicas,
		MaximumReplicas: *maximumReplicas,
		MinimumReplicas: *minimumReplicas,
		StartTime:       startTime,
		StopTime:        stopTime,
	}
}

func createScaledObject(namespace string, name string, config *ScalingConfig) kedav1alpha1.ScaledObject {
	return kedav1alpha1.ScaledObject{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: kedav1alpha1.ScaledObjectSpec{
			MaxReplicaCount: &config.MaximumReplicas,
			MinReplicaCount: &config.MinimumReplicas,
			ScaleTargetRef: &kedav1alpha1.ScaleTarget{
				Kind: "Deployment",
				Name: name,
			},
			Triggers: []kedav1alpha1.ScaleTriggers{
				kedav1alpha1.ScaleTriggers{
					Name: "cron",
					Type: "cron",
					Metadata: map[string]string{
						"desiredReplicas": strconv.Itoa(int(config.DesiredReplicas)),
						"end":             config.StopTime,
						"start":           config.StartTime,
						"timezone":        "Asia/Singapore",
					},
				},
			},
		},
	}
}

func createScalingConfigPatch(desired, current ScalingConfig) (patches []interface{}) {
	if desired.MaximumReplicas != current.MaximumReplicas {
		patches = append(patches, patchInt32Value{
			Op:    "replace",
			Path:  "/spec/maxReplicaCount",
			Value: desired.MaximumReplicas,
		})
	}
	if desired.MinimumReplicas != current.MinimumReplicas {
		patches = append(patches, patchInt32Value{
			Op:    "replace",
			Path:  "/spec/minReplicaCount",
			Value: desired.MinimumReplicas,
		})
	}
	if desired.DesiredReplicas != current.DesiredReplicas {
		patches = append(patches, patchStringValue{
			Op:    "replace",
			Path:  "/spec/triggers/0/metadata/desiredReplicas",
			Value: strconv.Itoa(int(desired.DesiredReplicas)),
		})
	}

	if desired.StartTime != current.StartTime {
		patches = append(patches, patchStringValue{
			Op:    "replace",
			Path:  "/spec/triggers/0/metadata/start",
			Value: desired.StartTime,
		})
	}
	if desired.StopTime != current.StopTime {
		patches = append(patches, patchStringValue{
			Op:    "replace",
			Path:  "/spec/triggers/0/metadata/end",
			Value: desired.StopTime,
		})
	}
	return
}
