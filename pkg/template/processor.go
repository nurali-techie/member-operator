package template

import (
	"context"
	"math/rand"
	"time"

	templatev1 "github.com/openshift/api/template/v1"
	"github.com/openshift/library-go/pkg/template/generator"
	"github.com/openshift/library-go/pkg/template/templateprocessing"
	"github.com/pkg/errors"
	errs "github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Processor the tool that will process and apply a template with variables
type Processor struct {
	cl     client.Client
	scheme *runtime.Scheme
}

// NewProcessor returns a new Processor
func NewProcessor(cl client.Client, scheme *runtime.Scheme) Processor {
	return Processor{cl: cl, scheme: scheme}
}

// Process processes the template (ie, replaces the variables with their actual values) and optionally filters the result
// to return a subset of the template objects
func (p Processor) Process(tmpl *templatev1.Template, values map[string]string, filters ...FilterFunc) ([]runtime.RawExtension, error) {
	// inject variables in the twmplate
	for param, val := range values {
		v := templateprocessing.GetParameterByName(tmpl, param)
		if v != nil {
			v.Value = val
			v.Generate = ""
		}
	}
	// convert the template into a set of objects
	tmplProcessor := templateprocessing.NewProcessor(map[string]generator.Generator{
		"expression": generator.NewExpressionValueGenerator(rand.New(rand.NewSource(time.Now().UnixNano()))),
	})
	if err := tmplProcessor.Process(tmpl); len(err) > 0 {
		return nil, errs.Wrap(err.ToAggregate(), "unable to process template")
	}
	var result templatev1.Template
	if err := p.scheme.Convert(tmpl, &result, nil); err != nil {
		return nil, errs.Wrap(err, "failed to convert template to external template object")
	}
	return Filter(result.Objects, filters...), nil
}

// Apply applies the objects, ie, creates or updates them on the cluster
func (p Processor) Apply(objs []runtime.RawExtension) error {
	for _, rawObj := range objs {
		obj := rawObj.Object
		if obj == nil {
			continue
		}
		gvk := obj.GetObjectKind().GroupVersionKind()
		if err := createOrUpdateObj(p.cl, obj); err != nil {
			return errs.Wrapf(err, "unable to create resource of kind: %s, version: %s", gvk.Kind, gvk.Version)
		}
	}
	return nil
}

func createOrUpdateObj(cl client.Client, obj runtime.Object) error {
	if err := cl.Create(context.TODO(), obj); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return errs.Wrapf(err, "failed to create object %v", obj)
		}
		if u, ok := obj.(*unstructured.Unstructured); ok {
			// get the existing NSTemplateTier
			existing := &unstructured.Unstructured{}
			existing.SetKind(u.GetKind())
			existing.SetAPIVersion(u.GetAPIVersion())
			err = cl.Get(context.TODO(), types.NamespacedName{
				Namespace: u.GetNamespace(),
				Name:      u.GetName(),
			}, existing)
			if err != nil {
				return errors.Wrapf(err, "unable to get the resource of kind '%s' and name '%s' in namespace '%s'", u.GetKind(), u.GetName(), u.GetNamespace())
			}
			// retrieve the current 'resourceVersion' to set it in the resource passed to the `client.Update()`
			// otherwise we would get an error with the following message:
			// "nstemplatetiers.toolchain.dev.openshift.com \"basic\" is invalid: metadata.resourceVersion: Invalid value: 0x0: must be specified for an update"
			u.SetResourceVersion(existing.GetResourceVersion())
			if err := cl.Update(context.TODO(), u); err != nil {
				return errors.Wrapf(err, "unable to update the resource of kind '%s' and name '%s' in namespace '%s'", u.GetKind(), u.GetName(), u.GetNamespace())
			}
		} else if err = cl.Update(context.TODO(), obj); err != nil {
			return errs.Wrapf(err, "failed to update object %v", obj)
		}
		return nil
	}
	return nil
}
