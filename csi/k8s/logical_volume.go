package k8s

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/cybozu-go/log"
	"github.com/cybozu-go/topolvm/csi"
	topolvmv1 "github.com/cybozu-go/topolvm/topolvm-node/api/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type LogicalVolumeService struct {
	k8sClient client.Client
	k8sCache  cache.Cache
	namespace string
}

func NewLogicalVolumeService(namespace string) (csi.LogicalVolumeService, error) {
	err := topolvmv1.AddToScheme(scheme.Scheme)
	if err != nil {
		return nil, err
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}

	k8sClient, err := client.New(config, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, err
	}

	cacheClient, err := cache.New(config, cache.Options{Scheme: scheme.Scheme})
	if err != nil {
		return nil, err
	}

	return &LogicalVolumeService{
		k8sClient: k8sClient,
		k8sCache:  cacheClient,
		namespace: namespace,
	}, nil
}

func (s *LogicalVolumeService) CreateVolume(ctx context.Context, node string, name string, sizeGb int64) (string, error) {
	log.Info("k8s.CreateVolume", map[string]interface{}{
		"name":    name,
		"node":    node,
		"size_gb": sizeGb,
	})

	wg := &sync.WaitGroup{}
	wg.Add(1)

	existingLV := new(topolvmv1.LogicalVolume)
	err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, existingLV)
	if client.IgnoreNotFound(err) != nil {
		return "", err
	} else if err == nil {
		// LV with same name was found; check compatibility
		// skip check of capabilities because (1) we allow both of two access types, and (2) we allow only one access mode
		// for ease of comparison, sizes are compared strictly, not by compatibility of ranges
		if existingLV.Spec.NodeName != node || existingLV.Spec.Size.Value() != sizeGb<<30 { // todo: add IsCompatibleWith() method to LogicalVolume?
			return "", status.Error(codes.AlreadyExists, "LogicalVolume already exists")
		}
		// compatible LV was found; check its phase
		// todo; if INITIAL, we should wait for CREATED in the polling below
	} else {
		lv := &topolvmv1.LogicalVolume{
			TypeMeta: metav1.TypeMeta{
				Kind:       "LogicalVolume",
				APIVersion: "topolvm.cybozu.com/v1",
			},
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: s.namespace,
			},
			Spec: topolvmv1.LogicalVolumeSpec{
				Name:     name,
				NodeName: node,
				Size:     *resource.NewQuantity(sizeGb<<30, resource.BinarySI),
			},
			Status: topolvmv1.LogicalVolumeStatus{
				Phase: "INITIAL",
			},
		}

		err := s.k8sClient.Create(ctx, lv)
		if err != nil {
			return "", err
		}
		log.Info("Created!!", nil)
	}

	//TODO: use informer
	for i := 0; i < 10; i++ {
		var newLV topolvmv1.LogicalVolume
		err := s.k8sClient.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: name}, &newLV)
		if err != nil {
			return "", err
		}
		if newLV.Status.Phase == "CREATED" {
			if newLV.Status.VolumeID == "" {
				return "", errors.New("VolumeID is empty")
			}
			return newLV.Status.VolumeID, nil
		}
		if newLV.Status.Phase == "CREATE_FAILED" {
			err := s.k8sClient.Delete(ctx, &newLV)
			if err != nil {
				// log this error but do not return this error, because newLV.Status.Message is more important
				log.Error("failed to delete LogicalVolume", map[string]interface{}{
					log.FnError: err,
				})
			}
			return "", status.Error(newLV.Status.Code, newLV.Status.Message)
		}
		time.Sleep(1 * time.Second)
	}

	return "", errors.New("timed out")
}

func (s *LogicalVolumeService) DeleteVolume(ctx context.Context, volumeID string) error {
	panic("implement me")
}

func (s *LogicalVolumeService) ExpandVolume(ctx context.Context, volumeID string, sizeGb int64) error {
	panic("implement me")
}
