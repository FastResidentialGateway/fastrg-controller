package storage

import (
	"context"
	"fmt"
)

// DeleteSubscriberDNS removes the DNS key owned by an HSI subscriber. The
// operation is intentionally a plain etcd delete: deleting an absent key is a
// successful no-op, which makes HSI deletion cascades safe to retry.
func DeleteSubscriberDNS(ctx context.Context, etcd *EtcdClient, nodeID, userID string) error {
	key := fmt.Sprintf("configs/%s/dns/%s", nodeID, userID)
	_, err := etcd.Client().Delete(ctx, key)
	return err
}
