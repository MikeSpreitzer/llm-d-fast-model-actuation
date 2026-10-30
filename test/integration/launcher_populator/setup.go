package launcher_populator

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	corev1 "k8s.io/api/core/v1"
	k8sapiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	k8sapiextclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeclient "k8s.io/client-go/kubernetes"

	fmav1alpha1 "github.com/llm-d-incubation/llm-d-fast-model-actuation/api/fma/v1alpha1"
	fmaconfig "github.com/llm-d-incubation/llm-d-fast-model-actuation/config"
	fmactlutils "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/controller/utils"
)

// Prep does the setup needed in the cluster before running the controller.
// This includes making sure that the CRDs are established.
func Prep(ctx context.Context, kubeClient kubeclient.Interface, kaeClient k8sapiextclient.Interface) error {
	scheme := k8sruntime.NewScheme()
	corev1.AddToScheme(scheme)
	k8sapiext.SchemeBuilder.AddToScheme(scheme)
	fmav1alpha1.SchemeBuilder.AddToScheme(scheme)
	codecs := serializer.NewCodecFactory(scheme)
	decoder := codecs.UniversalDecoder()
	crdOps := kaeClient.ApiextensionsV1().CustomResourceDefinitions()
	crds := []k8sapiext.CustomResourceDefinition{}
	crdsFS := fmaconfig.CRDsFS
	err := fs.WalkDir(crdsFS, ".", func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			return nil
		}
		crdBytes, err := crdsFS.ReadFile(path)
		if err != nil {
			return err
		}
		defaultGVK := k8sapiext.SchemeGroupVersion.WithKind("CustomResourceDefinition")
		var crdObj k8sapiext.CustomResourceDefinition
		decodedObj, _, err := decoder.Decode(crdBytes, &defaultGVK, &crdObj)
		if err != nil {
			return err
		}
		decodedCRD, ok := decodedObj.(*k8sapiext.CustomResourceDefinition)
		if !ok {
			return fmt.Errorf("decoded an object of type %T, expected %T", decodedObj, &crdObj)
		}
		createdCRD, err := crdOps.Get(ctx, decodedCRD.Name, metav1.GetOptions{})
		if err != nil {
			if !apierrors.IsNotFound(err) {
				return err
			}
			createdCRD, err = kaeClient.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, decodedCRD, metav1.CreateOptions{})
			if err != nil {
				return err
			}
		}
		crds = append(crds, *createdCRD)
		return nil
	})
	if err != nil {
		return err
	}
	err = wait.PollUntilContextTimeout(ctx, time.Second*3, time.Minute, false, func(ctx context.Context) (done bool, err error) {
		for _, crd := range crds {
			crdNow, err := crdOps.Get(ctx, crd.Name, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			cond := fmactlutils.SliceGetByFeature(crdNow.Status.Conditions, CRDConditionType, k8sapiext.Established)
			if cond == nil || cond.Status != k8sapiext.ConditionTrue {
				return false, nil
			}
		}
		return true, nil
	})
	return err
}

func CRDConditionType(cond *k8sapiext.CustomResourceDefinitionCondition) k8sapiext.CustomResourceDefinitionConditionType {
	return cond.Type
}
