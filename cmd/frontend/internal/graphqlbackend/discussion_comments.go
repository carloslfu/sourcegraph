package graphqlbackend

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	graphql "github.com/graph-gophers/graphql-go"
	"github.com/pkg/errors"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/backend"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/db"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/pkg/discussions"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/pkg/markdown"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/internal/pkg/types"
)

type discussionCommentResolver struct {
	c *types.DiscussionComment
}

func (r *discussionCommentResolver) ID() graphql.ID {
	return marshalDiscussionID(r.c.ID)
}

func (r *discussionCommentResolver) Thread(ctx context.Context) (*discussionThreadResolver, error) {
	thread, err := db.DiscussionThreads.Get(ctx, r.c.ThreadID)
	if err != nil {
		return nil, errors.Wrap(err, "DiscussionThreads.Get")
	}
	return &discussionThreadResolver{t: thread}, nil
}

func (r *discussionCommentResolver) Author(ctx context.Context) (*userResolver, error) {
	return userByIDInt32(ctx, r.c.AuthorUserID)
}

func (r *discussionCommentResolver) Contents(ctx context.Context) (string, error) {
	if strings.TrimSpace(r.c.Contents) != "" {
		return r.c.Contents, nil
	}
	thread, err := db.DiscussionThreads.Get(ctx, r.c.ThreadID)
	if err != nil {
		return "", errors.Wrap(err, "DiscussionThreads.Get")
	}
	return thread.Title, nil
}
func (r *discussionCommentResolver) HTML(ctx context.Context, args *struct{ Options *markdownOptions }) (string, error) {
	contents, err := r.Contents(ctx)
	if err != nil {
		return "", err
	}
	return markdown.Render(contents, nil), nil
}
func (r *discussionCommentResolver) InlineURL(ctx context.Context) (*string, error) {
	thread, err := db.DiscussionThreads.Get(ctx, r.c.ThreadID)
	if err != nil {
		return nil, errors.Wrap(err, "DiscussionThreads.Get")
	}
	url, err := discussions.URLToInlineComment(ctx, thread, r.c)
	if err != nil {
		return nil, err
	}
	return strptr(url.String()), nil
}
func (r *discussionCommentResolver) CreatedAt(ctx context.Context) string {
	return r.c.CreatedAt.Format(time.RFC3339)
}
func (r *discussionCommentResolver) UpdatedAt(ctx context.Context) string {
	return r.c.UpdatedAt.Format(time.RFC3339)
}
func (r *discussionCommentResolver) Reports(ctx context.Context) []string {
	// 🚨 SECURITY: Only site admins can read reports.
	if err := backend.CheckCurrentUserIsSiteAdmin(ctx); err != nil {
		return []string{}
	}
	return r.c.Reports
}

func (s *schemaResolver) DiscussionComments(ctx context.Context, args *struct {
	connectionArgs
	AuthorUserID *graphql.ID
}) (*discussionCommentsConnectionResolver, error) {
	if err := viewerCanUseDiscussions(ctx); err != nil {
		return nil, err
	}

	// 🚨 SECURITY: No authentication is required to list the comments on a
	// discussion. They are public unless the Sourcegraph instance itself (and
	// inherently, the GraphQL API) is private.

	opt := &db.DiscussionCommentsListOptions{}
	args.connectionArgs.set(&opt.LimitOffset)
	if args.AuthorUserID != nil {
		userID, err := unmarshalUserID(*args.AuthorUserID)
		if err != nil {
			return nil, err
		}
		opt.AuthorUserID = &userID
	}
	return &discussionCommentsConnectionResolver{opt: opt}, nil
}

func (r *discussionsMutationResolver) AddCommentToThread(ctx context.Context, args *struct {
	ThreadID graphql.ID
	Contents string
}) (*discussionThreadResolver, error) {
	// 🚨 SECURITY: Only signed in users may add comments to a discussion thread.
	currentUser, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if currentUser == nil {
		return nil, errors.New("no current user")
	}

	if strings.TrimSpace(args.Contents) == "" {
		return nil, errors.New("cannot add empty comments to threads")
	}

	// Create the comment on the thread.
	threadID, err := unmarshalDiscussionID(args.ThreadID)
	if err != nil {
		return nil, err
	}
	// TODO(slimsag:discussions): Unify this discussion thread creation code with the mailreply worker?
	newComment := &types.DiscussionComment{
		ThreadID:     threadID,
		AuthorUserID: currentUser.user.ID,
		Contents:     args.Contents,
	}
	_, err = db.DiscussionComments.Create(ctx, newComment)
	if err != nil {
		return nil, errors.Wrap(err, "DiscussionComments.Create")
	}

	// Fetch and return the updated thread object.
	updatedThread, err := db.DiscussionThreads.Get(ctx, threadID)
	if err != nil {
		return nil, errors.Wrap(err, "DiscussionThreads.Get")
	}
	discussions.NotifyNewComment(updatedThread, newComment)
	return &discussionThreadResolver{t: updatedThread}, nil
}

func (r *discussionsMutationResolver) UpdateComment(ctx context.Context, args *struct {
	Input *struct {
		CommentID    graphql.ID
		Contents     *string
		Delete       *bool
		Report       *string
		ClearReports *bool
	}
}) (*discussionThreadResolver, error) {
	commentID, err := unmarshalDiscussionID(args.Input.CommentID)
	if err != nil {
		return nil, err
	}

	// 🚨 SECURITY: Only signed in users may update a discussion comment.
	currentUser, err := currentUser(ctx)
	if err != nil {
		return nil, err
	}
	if currentUser == nil {
		return nil, errors.New("no current user")
	}

	var delete bool
	if args.Input.Delete != nil && *args.Input.Delete {
		// 🚨 SECURITY: Only site admins can delete discussion comments.
		if err := backend.CheckCurrentUserIsSiteAdmin(ctx); err != nil {
			return nil, err
		}
		delete = *args.Input.Delete
	}

	var clearReports bool
	if args.Input.ClearReports != nil && *args.Input.ClearReports {
		// 🚨 SECURITY: Only site admins can clear reports.
		if err := backend.CheckCurrentUserIsSiteAdmin(ctx); err != nil {
			return nil, err
		}
		clearReports = *args.Input.ClearReports
	}

	if args.Input.Report != nil {
		newReport := fmt.Sprintf("%s\n\nreported by @%s", *args.Input.Report, currentUser.user.Username)
		args.Input.Report = &newReport
	}

	if args.Input.Contents != nil {
		// 🚨 SECURITY: Only site admins and the comment author can update the contents.
		comment, err := db.DiscussionComments.Get(ctx, commentID)
		if err != nil {
			return nil, err
		}
		err = backend.CheckSiteAdminOrSameUser(ctx, comment.AuthorUserID)
		if err != nil {
			return nil, err
		}
	}

	// Resolve the thread ID of the comment first so we can return the updated
	// thread later. We must do this now because the comment may be deleted
	// below (Update may return nil).
	comment, err := db.DiscussionComments.Get(ctx, commentID)
	if err != nil {
		return nil, errors.Wrap(err, "DiscussionComments.Get")
	}
	threadID := comment.ThreadID

	_, err = db.DiscussionComments.Update(ctx, commentID, &db.DiscussionCommentsUpdateOptions{
		Contents:     args.Input.Contents,
		Delete:       delete,
		Report:       args.Input.Report,
		ClearReports: clearReports,
	})
	if err != nil {
		return nil, errors.Wrap(err, "DiscussionComments.Update")
	}
	thread, err := db.DiscussionThreads.Get(ctx, threadID)
	if err != nil {
		return nil, errors.Wrap(err, "DiscussionThreads.Get")
	}
	return &discussionThreadResolver{t: thread}, nil
}

// discussionCommentsConnectionResolver resolves a list of discussion comments.
//
// 🚨 SECURITY: When instantiating an discussionCommentsConnectionResolver
// value, the caller MUST check permissions.
type discussionCommentsConnectionResolver struct {
	opt *db.DiscussionCommentsListOptions

	// cache results because they are used by multiple fields
	once     sync.Once
	comments []*types.DiscussionComment
	err      error
}

func (r *discussionCommentsConnectionResolver) compute(ctx context.Context) ([]*types.DiscussionComment, error) {
	r.once.Do(func() {
		opt2 := *r.opt
		if opt2.LimitOffset != nil {
			tmp := *opt2.LimitOffset
			opt2.LimitOffset = &tmp
			opt2.Limit++ // so we can detect if there is a next page
		}

		r.comments, r.err = db.DiscussionComments.List(ctx, &opt2)
	})
	return r.comments, r.err
}

func (r *discussionCommentsConnectionResolver) Nodes(ctx context.Context) ([]*discussionCommentResolver, error) {
	comments, err := r.compute(ctx)
	if err != nil {
		return nil, err
	}

	var l []*discussionCommentResolver
	for _, comment := range comments {
		l = append(l, &discussionCommentResolver{c: comment})
	}
	return l, nil
}

func (r *discussionCommentsConnectionResolver) TotalCount(ctx context.Context) (int32, error) {
	withoutLimit := *r.opt
	withoutLimit.LimitOffset = nil
	count, err := db.DiscussionComments.Count(ctx, &withoutLimit)
	return int32(count), err
}

func (r *discussionCommentsConnectionResolver) PageInfo(ctx context.Context) (*pageInfo, error) {
	comments, err := r.compute(ctx)
	if err != nil {
		return nil, err
	}
	return &pageInfo{hasNextPage: r.opt.LimitOffset != nil && len(comments) > r.opt.Limit}, nil
}
