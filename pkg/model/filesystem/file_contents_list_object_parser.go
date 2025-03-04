package filesystem

import (
	"context"
	"math"

	"github.com/buildbarn/bb-storage/pkg/util"
	model_core "github.com/buildbarn/bonanza/pkg/model/core"
	"github.com/buildbarn/bonanza/pkg/model/parser"
	model_parser "github.com/buildbarn/bonanza/pkg/model/parser"
	model_filesystem_pb "github.com/buildbarn/bonanza/pkg/proto/model/filesystem"
	"github.com/buildbarn/bonanza/pkg/storage/object"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// FileContentsEntry contains the properties of a part of a concatenated file.
type FileContentsEntry[TReference any] struct {
	EndBytes  uint64
	Reference TReference
}

// NewFileContentsEntryFromProto constructs a FileContentsListEntry
// based on the contents of a single FileContents Protobuf message,
// refering to the file as a whole.
func NewFileContentsEntryFromProto[
	TReference any,
	TReferenceFormat interface {
		GetBogusReference() TReference
	},
](fileContents model_core.Message[*model_filesystem_pb.FileContents, object.OutgoingReferences[TReference]], referenceFormat TReferenceFormat) (FileContentsEntry[TReference], error) {
	if fileContents.Message == nil {
		// File is empty, meaning that it is not backed by any
		// object. Map all of such files to the bogus reference.
		// We assume that specifying a size of zero is enough to
		// prevent the underlying file implementation from
		// loading any objects from storage.
		return FileContentsEntry[TReference]{
			EndBytes:  0,
			Reference: referenceFormat.GetBogusReference(),
		}, nil
	}

	reference, err := model_core.FlattenReference(model_core.NewNestedMessage(fileContents, fileContents.Message.Reference))
	if err != nil {
		return FileContentsEntry[TReference]{}, err
	}
	return FileContentsEntry[TReference]{
		EndBytes:  fileContents.Message.TotalSizeBytes,
		Reference: reference,
	}, nil
}

// FileContentsList contains the properties of parts of a concatenated
// file. Parts are stored in the order in which they should be
// concatenated, with EndBytes increasing.
type FileContentsList[TReference any] []FileContentsEntry[TReference]

// FileContentsListObjectParserReference is a constraint on the
// reference types accepted by the ObjectParser returned by
// NewFileContentsListObjectParser.
type FileContentsListObjectParserReference[T any] interface {
	GetSizeBytes() int
}

type fileContentsListObjectParser[TReference FileContentsListObjectParserReference[TReference]] struct{}

// NewFileContentsListObjectParser creates an ObjectParser that is
// capable of parsing FileContentsList messages, turning them into a
// list of entries that can be processed by FileContentsIterator.
func NewFileContentsListObjectParser[TReference FileContentsListObjectParserReference[TReference]]() parser.ObjectParser[TReference, FileContentsList[TReference]] {
	return &fileContentsListObjectParser[TReference]{}
}

func (p *fileContentsListObjectParser[TReference]) ParseObject(ctx context.Context, reference TReference, outgoingReferences object.OutgoingReferences[TReference], data []byte) (FileContentsList[TReference], int, error) {
	l, sizeBytes, err := model_parser.NewMessageListObjectParser[TReference, model_filesystem_pb.FileContents]().
		ParseObject(ctx, reference, outgoingReferences, data)
	if err != nil {
		return nil, 0, err
	}
	if len(l.Message) < 2 {
		return nil, 0, status.Error(codes.InvalidArgument, "File contents list contains fewer than two parts")
	}

	var endBytes uint64
	fileContentsList := make(FileContentsList[TReference], 0, len(l.Message))
	for i, part := range l.Message {
		// Convert 'total_size_bytes' to a cumulative value, to
		// allow FileContentsIterator to perform binary searching.
		if part.TotalSizeBytes < 1 {
			return nil, 0, status.Errorf(codes.InvalidArgument, "Part at index %d does not contain any data", i)
		}
		if part.TotalSizeBytes > math.MaxUint64-endBytes {
			return nil, 0, status.Errorf(codes.InvalidArgument, "Combined size of all parts exceeds maximum file size of %d bytes", uint64(math.MaxUint64))
		}
		endBytes += part.TotalSizeBytes

		partReference, err := model_core.FlattenReference(model_core.NewNestedMessage(l, part.Reference))
		if err != nil {
			return nil, 0, util.StatusWrapf(err, "Invalid reference for part at index %d", i)
		}

		fileContentsList = append(fileContentsList, FileContentsEntry[TReference]{
			EndBytes:  endBytes,
			Reference: partReference,
		})
	}
	return fileContentsList, sizeBytes, nil
}
