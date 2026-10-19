package requestbuild

import (
	"io"
	"mime/multipart"
	"net/textproto"
	"strings"

	"github.com/razrabotchik/lotsman/internal/domain"
	"github.com/razrabotchik/lotsman/internal/errs"
	"github.com/razrabotchik/lotsman/internal/fileinput"
)

// fileBody serializes a body made of file content (feature 009).
//
// Two properties decide the shape of this code. The content streams from disk,
// so the process holds a copy buffer and not a file; and the request declares
// a Content-Length, because the reference backend is PHP behind Apache or
// nginx, where a chunked upload arrives with an empty $_FILES (ADR-0022
// decision 5). Those two together mean the exact length has to be known before
// the first byte is written -- which it is: a boundary, the part headers, the
// text values and each file's fstat size are all known once the files are
// open.
func fileBody(spec *domain.BodySpec, value any, opts Options) (encodedBody, error) {
	if opts.Files == nil {
		// Publication depends on the resolver existing, so this is a wiring
		// mistake rather than a refusal an operator or a caller can act on.
		return encodedBody{}, errs.Errorf(errs.ClassInternal,
			"requestbuild: this operation sends a %s body and no file resolver was configured",
			spec.MediaType)
	}
	session := opts.Files.Session()
	body, err := func() (encodedBody, error) {
		if spec.BodyKindOf() == domain.BodyBinary {
			return singleFileBody(spec, value, session)
		}
		return multipartBody(spec, value, session, opts.Boundary)
	}()
	if err != nil {
		// Nothing was sent, so nothing stays open: a refused call must not
		// leave a descriptor behind (ADR-0022).
		_ = session.Close()
		return encodedBody{}, err
	}
	body.files = session
	return body, nil
}

// singleFileBody sends one file as the whole body.
func singleFileBody(spec *domain.BodySpec, value any, session *fileinput.Session) (encodedBody, error) {
	file, err := openArgument(spec.MediaType, "body", value, nil, session)
	if err != nil {
		return encodedBody{}, err
	}
	return encodedBody{
		reader:      file.Reader(),
		contentType: spec.MediaType,
		length:      file.Size(),
	}, nil
}

// multipartBody assembles the parts, measures them, and returns a body that
// streams.
func multipartBody(spec *domain.BodySpec, value any, session *fileinput.Session, boundary string) (encodedBody, error) {
	fields, ok := value.(map[string]any)
	if !ok {
		return encodedBody{}, errs.Errorf(errs.ClassUsage,
			"requestbuild: a %s body is a set of named parts, and this argument is %T",
			spec.MediaType, value)
	}

	plan := make([]plannedPart, 0, len(spec.Parts))
	for _, part := range spec.Parts {
		argument, present := fields[part.Name]
		if !present {
			// The schema decided what may be absent; an absent optional part
			// is simply not sent, which is what a form without that field is.
			continue
		}
		if part.Binary {
			file, err := openArgument(spec.MediaType, part.Name, argument, part.ContentTypes, session)
			if err != nil {
				return encodedBody{}, err
			}
			plan = append(plan, plannedPart{name: part.Name, file: file})
			continue
		}
		text, err := scalarString(part.Name, argument)
		if err != nil {
			return encodedBody{}, err
		}
		contentType := ""
		if len(part.ContentTypes) == 1 {
			contentType = part.ContentTypes[0]
		}
		plan = append(plan, plannedPart{name: part.Name, text: text, contentType: contentType})
	}
	for _, field := range sortedNames(fields) {
		if !declaredPart(spec.Parts, field) {
			return encodedBody{}, errs.Errorf(errs.ClassUsage,
				"requestbuild: this operation declares no %s part named %q", spec.MediaType, field)
		}
	}

	// The length is measured by writing the same parts to a counter, with the
	// file content left out and added from its fstat size. Same boundary, same
	// headers, same writer: the number is the body's, not an estimate of it.
	counter := &countingWriter{}
	measure := multipart.NewWriter(counter)
	if boundary != "" {
		if err := measure.SetBoundary(boundary); err != nil {
			return encodedBody{}, errs.Errorf(errs.ClassInternal, "requestbuild: %w", err)
		}
	}
	if err := writeParts(measure, plan, false); err != nil {
		return encodedBody{}, err
	}
	if err := measure.Close(); err != nil {
		return encodedBody{}, errs.Errorf(errs.ClassInternal, "requestbuild: %w", err)
	}
	length := counter.n
	for _, part := range plan {
		if part.file != nil {
			length += part.file.Size()
		}
	}

	reader, writer := io.Pipe()
	stream := multipart.NewWriter(writer)
	if err := stream.SetBoundary(measure.Boundary()); err != nil {
		return encodedBody{}, errs.Errorf(errs.ClassInternal, "requestbuild: %w", err)
	}
	go func() {
		err := writeParts(stream, plan, true)
		if err == nil {
			err = stream.Close()
		}
		// CloseWithError(nil) closes the pipe cleanly; an error here reaches
		// the transport as a failed body, which is what a file that changed
		// under us has to do -- the alternative is a body that does not match
		// its Content-Length.
		_ = writer.CloseWithError(err)
	}()

	return encodedBody{
		reader:      reader,
		contentType: stream.FormDataContentType(),
		length:      length,
		closer:      reader,
	}, nil
}

// plannedPart is one part, resolved: a text value, or an open file.
type plannedPart struct {
	name        string
	text        string
	contentType string
	file        *fileinput.File
}

// writeParts writes the parts in order. With content false it writes
// everything but the file bytes, which is how the body is measured; the two
// calls must stay one function so that what was measured is what is sent.
func writeParts(w *multipart.Writer, plan []plannedPart, content bool) error {
	buffer := fileinput.CopyBuffer()
	for _, part := range plan {
		header := make(textproto.MIMEHeader, 2)
		switch {
		case part.file != nil:
			// Built by concatenation rather than with %q: a non-ASCII
			// filename is sent as UTF-8 per RFC 7578, and Go's quoting would
			// escape it into something else.
			header.Set("Content-Disposition", `form-data; name="`+escapeHeaderQuotes(part.name)+
				`"; filename="`+escapeHeaderQuotes(part.file.Name())+`"`)
			header.Set("Content-Type", part.file.ContentType())
		default:
			header.Set("Content-Disposition", `form-data; name="`+escapeHeaderQuotes(part.name)+`"`)
			if part.contentType != "" {
				header.Set("Content-Type", part.contentType)
			}
		}
		slot, err := w.CreatePart(header)
		if err != nil {
			return errs.Errorf(errs.ClassInternal, "requestbuild: %w", err)
		}
		switch {
		case part.file == nil:
			if _, err := io.WriteString(slot, part.text); err != nil {
				return errs.Errorf(errs.ClassInternal, "requestbuild: %w", err)
			}
		case content:
			// CopyBuffer, so the window is the buffer and not the file: the
			// one number this process holds for a 20 MiB upload.
			if _, err := io.CopyBuffer(slot, part.file.Reader(), buffer); err != nil {
				return err
			}
		}
	}
	return nil
}

// openArgument turns a file-input argument into an open file.
func openArgument(mediaType, part string, value any, contentTypes []string, session *fileinput.Session) (*fileinput.File, error) {
	fields, ok := value.(map[string]any)
	if !ok {
		return nil, errs.Errorf(errs.ClassUsage,
			"requestbuild: %s part %q takes an object naming a local file (a path), and this argument is %T",
			mediaType, part, value)
	}
	request := fileinput.Request{PartContentTypes: contentTypes}
	for _, field := range []struct {
		name  string
		slot  *string
		known bool
	}{
		{"path", &request.Path, true},
		{"filename", &request.Filename, true},
		{"contentType", &request.ContentType, true},
	} {
		raw, present := fields[field.name]
		if !present {
			continue
		}
		text, isText := raw.(string)
		if !isText {
			return nil, errs.Errorf(errs.ClassUsage,
				"requestbuild: %s of %s part %q is %T, and it names a string", field.name, mediaType, part, raw)
		}
		*field.slot = text
	}
	for name := range fields {
		switch name {
		case "path", "filename", "contentType":
		default:
			return nil, errs.Errorf(errs.ClassUsage,
				"requestbuild: %s part %q has no field %q; a file is named by path, filename and contentType",
				mediaType, part, name)
		}
	}
	return session.Open(request)
}

// escapeHeaderQuotes is what mime/multipart does to a name in a header
// parameter, repeated here because the headers are written by hand: a
// quotation mark or a backslash in a filename must not end the parameter.
// CR and LF cannot appear -- fileinput refuses a control character in a
// filename -- and a part name comes from the document, which is checked at
// publication.
func escapeHeaderQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
}

// countingWriter counts bytes and keeps none.
type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func declaredPart(parts []domain.BodyPart, name string) bool {
	for _, part := range parts {
		if part.Name == name {
			return true
		}
	}
	return false
}

func sortedNames(fields map[string]any) []string {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	// Sorted, so the message names the same unexpected field every time.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}
