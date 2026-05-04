package controllerpb

//go:generate protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative --go-grpc_opt=require_unimplemented_servers=false controller.proto
//go:generate sh -c "mkdir -p fastrgnodepb && cd fastrgnodepb && (test -f fastrg_node.proto || curl -sSL https://raw.githubusercontent.com/FastResidentialGateway/fastrg-node/master/northbound/grpc/fastrg_node.proto -o fastrg_node.proto) && protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative --go-grpc_opt=require_unimplemented_servers=false fastrg_node.proto"
