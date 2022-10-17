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
	"fmt"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/policy"
	"github.com/minio/minio-go/v7/pkg/set"
	miniov1alpha1 "github.com/reddec/minio-ext-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/json"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const bucketFinalizer = "reddec.net.k8s.minio-bucket-finalizer"

// BucketReconciler reconciles a Bucket object
type BucketReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Minio  *minio.Client
}

//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=buckets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=buckets/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=buckets/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *BucketReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var manifest *miniov1alpha1.Bucket
	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, manifest); err != nil {
		return ctrl.Result{}, fmt.Errorf("get manifest: %w", err)
	}

	// removal
	if manifest.GetDeletionTimestamp() != nil {
		if err := r.removeBucket(ctx, manifest); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove bucket: %w", err)
		}
		controllerutil.RemoveFinalizer(manifest, bucketFinalizer)
		if err := r.Update(ctx, manifest); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// add finalizer
	if !controllerutil.ContainsFinalizer(manifest, bucketFinalizer) {
		controllerutil.AddFinalizer(manifest, bucketFinalizer)
		if err := r.Update(ctx, manifest); err != nil {
			return ctrl.Result{}, err
		}
	}

	// always create bucket
	if exist, err := r.Minio.BucketExists(ctx, manifest.Name); err != nil {
		return ctrl.Result{}, fmt.Errorf("check bucket: %w", err)
	} else if !exist {
		if err := r.Minio.MakeBucket(ctx, manifest.Name, minio.MakeBucketOptions{}); err != nil {
			return ctrl.Result{}, fmt.Errorf("create bucket: %w", err)
		}
	}
	meta.SetStatusCondition(&manifest.Status.Conditions, metav1.Condition{
		Type:   miniov1alpha1.BucketConditionCreated,
		Status: "true",
	})
	if err := r.Update(ctx, manifest); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	// always set policy
	if err := r.setBucketPolicy(ctx, manifest); err != nil {
		return ctrl.Result{}, fmt.Errorf("set bucket policy: %w", err)
	}
	meta.SetStatusCondition(&manifest.Status.Conditions, metav1.Condition{
		Type:   miniov1alpha1.BucketConditionPolicyAssigned,
		Status: "true",
	})
	if err := r.Update(ctx, manifest); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	return ctrl.Result{Requeue: true, RequeueAfter: time.Minute}, nil
}

func (r *BucketReconciler) setBucketPolicy(ctx context.Context, manifest *miniov1alpha1.Bucket) error {
	return r.Minio.SetBucketPolicy(ctx, manifest.Name, mustPolicy(manifest))
}

func (r *BucketReconciler) removeBucket(ctx context.Context, manifest *miniov1alpha1.Bucket) error {
	if manifest.Spec.Retain {
		return nil
	}
	return r.Minio.RemoveBucketWithOptions(ctx, manifest.Name, minio.RemoveBucketOptions{ForceDelete: true})
}

// SetupWithManager sets up the controller with the Manager.
func (r *BucketReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&miniov1alpha1.Bucket{}).
		Complete(r)
}

func mustPolicy(manifest *miniov1alpha1.Bucket) string {
	var p = policy.BucketAccessPolicy{
		Version:    "2012-10-17",
		Statements: []policy.Statement{},
	}

	if manifest.Spec.Public {
		p.Statements = append(p.Statements, policy.Statement{
			Actions: set.CreateStringSet("s3:GetObject"),
			Effect:  "Allow",
			Principal: policy.User{
				AWS: set.CreateStringSet("*"),
			},
			Resources: set.CreateStringSet("arn:aws:s3:::" + manifest.Name + "/*"),
		})
	}

	for _, access := range manifest.Spec.Access {
		var perms set.StringSet
		if access.Read && access.Write {
			perms = set.CreateStringSet("s3:*")
		} else if access.Read {
			perms = readRights()
		} else if access.Write {
			perms = set.CreateStringSet("s3:PutObject")
		}
		p.Statements = append(p.Statements, policy.Statement{
			Actions: perms,
			Effect:  "Allow",
			Principal: policy.User{
				AWS: set.CreateStringSet("*"),
			},
			Resources: set.CreateStringSet("arn:aws:s3:::" + manifest.Name + "/*"),
		})
	}
	data, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func readRights() set.StringSet {
	return set.CreateStringSet(
		"s3:GetBucketLocation",
		"s3:GetObject",
		"s3:ListBucket",
		"s3:ListenNotification",
		"s3:ListenBucketNotification",
	)
}
