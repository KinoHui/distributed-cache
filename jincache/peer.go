package jincache

import pb "github.com/KinoHui/distributed-cache/jincache/jincachepb"

// PeerPicker is the interface that must be implemented to locate
// the peer that owns a specific key.
type PeerPicker interface {
	PickPeer(key string) (peer PeerClient, ok bool)
}

// PeerClient is the interface that must be implemented by a peer.
type PeerClient interface {
	Get(in *pb.Request, out *pb.Response) error
	Set(in *pb.Request) error
	Delete(in *pb.Request) error
}
