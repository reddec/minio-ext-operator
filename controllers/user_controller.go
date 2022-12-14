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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/minio/madmin-go"
	miniov1alpha1 "github.com/reddec/minio-ext-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	errors2 "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const userFinalizer = "reddec.net.k8s.minio-user-finalizer"
const secretSize = 32

// UserReconciler reconciles a User object
type UserReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Admin  *madmin.AdminClient
}

//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=users,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=users/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=minio.k8s.reddec.net,namespace=minio,resources=users/finalizers,verbs=update
//+kubebuilder:rbac:groups="",resources=secrets,namespace=minio,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *UserReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var manifest miniov1alpha1.User
	if err := r.Get(ctx, client.ObjectKey{Namespace: req.Namespace, Name: req.Name}, &manifest); err != nil {
		if errors2.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get manifest: %w", err)
	}

	// removal
	if manifest.GetDeletionTimestamp() != nil {
		logger.Info("removing user")
		if err := r.removeUser(ctx, &manifest); err != nil {
			return ctrl.Result{}, fmt.Errorf("remove user: %w", err)
		}
		controllerutil.RemoveFinalizer(&manifest, userFinalizer)
		if err := r.Update(ctx, &manifest); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// add finalizer
	if !controllerutil.ContainsFinalizer(&manifest, userFinalizer) {
		controllerutil.AddFinalizer(&manifest, userFinalizer)
		if err := r.Update(ctx, &manifest); err != nil {
			return ctrl.Result{}, err
		}
	}

	// create or update secret
	logger.Info("creating secret")
	secret, err := r.createOrUpdateSecret(ctx, &manifest)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("set secret: %w", err)
	}
	meta.SetStatusCondition(&manifest.Status.Conditions, metav1.Condition{
		Type:   miniov1alpha1.UserConditionSecretCreated,
		Status: "true",
	})
	if err := r.Update(ctx, &manifest); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status (2): %w", err)
	}

	// create user
	logger.Info("creating user")
	if err := r.Admin.AddUser(ctx, manifest.Name, secret); err != nil {
		return ctrl.Result{}, fmt.Errorf("create user: %w", err)
	}
	meta.SetStatusCondition(&manifest.Status.Conditions, metav1.Condition{
		Type:   miniov1alpha1.UserConditionCreated,
		Status: "true",
	})
	if err := r.Update(ctx, &manifest); err != nil {
		return ctrl.Result{}, fmt.Errorf("update status: %w", err)
	}

	// update user
	if err := r.Admin.SetUser(ctx, manifest.Name, secret, madmin.AccountEnabled); err != nil {
		return ctrl.Result{}, fmt.Errorf("update user: %w", err)
	}

	return ctrl.Result{RequeueAfter: time.Minute, Requeue: true}, nil
}

func (r *UserReconciler) removeUser(ctx context.Context, manifest *miniov1alpha1.User) error {
	err := r.Admin.RemoveUser(ctx, manifest.Name)
	if err == nil {
		return nil
	}
	if v, ok := err.(madmin.ErrorResponse); ok && v.Code == "XMinioAdminNoSuchUser" {
		return nil
	}
	return fmt.Errorf("remove user: %w", err)
}

func (r *UserReconciler) createOrUpdateSecret(ctx context.Context, manifest *miniov1alpha1.User) (string, error) {
	var secret = &v1.Secret{}
	err := r.Get(ctx, client.ObjectKey{Namespace: manifest.Namespace, Name: manifest.SecretName()}, secret)
	if err == nil {
		_, hasName := secret.Data["AWS_ACCESS_KEY_ID"]
		if pass, hasSecret := secret.Data["AWS_SECRET_ACCESS_KEY"]; hasSecret && hasName && len(pass) == 2*secretSize {
			return string(pass), nil
		}
		pass := mustGetSecret(secretSize)
		secret.Data = map[string][]byte{
			"AWS_ACCESS_KEY_ID":     []byte(manifest.Name),
			"AWS_SECRET_ACCESS_KEY": []byte(pass),
		}
		return pass, r.Update(ctx, secret)
	}
	if !errors2.IsNotFound(err) {
		return "", err
	}
	pass := mustGetSecret(secretSize)
	secret = &v1.Secret{
		ObjectMeta: ctrl.ObjectMeta{
			Name:      manifest.SecretName(),
			Namespace: manifest.Namespace,
		},
		Data: map[string][]byte{
			"AWS_ACCESS_KEY_ID":     []byte(manifest.Name),
			"AWS_SECRET_ACCESS_KEY": []byte(pass),
		},
	}

	if err := ctrl.SetControllerReference(manifest, secret, r.Scheme); err != nil {
		return "", fmt.Errorf("set controller refrence: %w", err)
	}
	return pass, r.Create(ctx, secret)
}

// SetupWithManager sets up the controller with the Manager.
func (r *UserReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&miniov1alpha1.User{}).
		Owns(&v1.Secret{}).
		Complete(r)
}

func mustGetSecret(bytes int) string {
	var buf = make([]byte, bytes)
	_, err := io.ReadFull(rand.Reader, buf)
	if err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
