/*
Copyright 2022 Aleksandr Baryshnikov.

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

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/minio/madmin-go"
	"github.com/minio/minio-go/v7/pkg/policy"
	"github.com/minio/minio-go/v7/pkg/set"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	miniov1alpha1 "github.com/reddec/minio-ext-operator/api/v1alpha1"
)

// PolicyReconciler reconciles a Policy object
type PolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Admin  *madmin.AdminClient
}

const policyFinalizer = "reddec.net.k8s.minio-policy-finalizer"

//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=policies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=policies/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=policies/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *PolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	var manifest = &miniov1alpha1.Policy{}
	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, manifest); err != nil {
		if errors2.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get manifest: %w", err)
	}

	// removal
	if manifest.GetDeletionTimestamp() != nil {
		logger.Info("removing policy")
		if err := r.Admin.RemoveCannedPolicy(ctx, manifest.Name); err != nil {
			if merr, ok := err.(madmin.ErrorResponse); ok && merr.Code == "XMinioErrAdminNoSuchPolicy" {
				logger.Info("policy already removed")
			} else {
				return ctrl.Result{}, fmt.Errorf("remove bucket: %w", err)
			}
		}
		controllerutil.RemoveFinalizer(manifest, policyFinalizer)
		if err := r.Update(ctx, manifest); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// add finalizer
	if !controllerutil.ContainsFinalizer(manifest, policyFinalizer) {
		controllerutil.AddFinalizer(manifest, policyFinalizer)
		if err := r.Update(ctx, manifest); err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.Info("creating policy")
	if err := r.Admin.AddCannedPolicy(ctx, manifest.Name, mustIAMPolicy(manifest)); err != nil {
		return ctrl.Result{}, fmt.Errorf("add policy: %w", err)
	}

	logger.Info("assigning policy")
	if err := r.Admin.SetPolicy(ctx, manifest.Name, manifest.Spec.User, false); err != nil {
		if merr, ok := err.(madmin.ErrorResponse); ok && merr.Code == "XMinioAdminNoSuchUser" {
			logger.Info("no such user, retrying later")
			return ctrl.Result{Requeue: true, RequeueAfter: 10 * time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("set policy: %w", err)
	}
	return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&miniov1alpha1.Policy{}).
		Complete(r)
}

func mustIAMPolicy(manifest *miniov1alpha1.Policy) []byte {
	var p = policy.BucketAccessPolicy{
		Version:    "2012-10-17",
		Statements: []policy.Statement{},
	}

	var perms set.StringSet
	if manifest.Spec.Read && manifest.Spec.Write {
		perms = set.CreateStringSet("s3:*")
	} else if manifest.Spec.Read {
		perms = readRights()
	} else if manifest.Spec.Write {
		perms = set.CreateStringSet("s3:PutObject")
	}
	p.Statements = append(p.Statements, policy.Statement{
		Actions: perms,
		Effect:  "Allow",
		Principal: policy.User{
			AWS: set.CreateStringSet(manifest.Spec.User),
		},
		Resources: set.CreateStringSet("arn:aws:s3:::" + manifest.Name + "/*"),
	})

	data, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return data
}
