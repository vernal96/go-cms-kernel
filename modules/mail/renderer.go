package mail

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
	"github.com/vernal96/go-cms-kernel/templating"
)

const (
	DefaultMaxTemplateLength = 1 << 20
	DefaultMaxResultLength   = 4 << 20
)

var templateCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

type FileService interface {
	GetFile(context.Context, security.Actor, file.ID) (file.File, error)
	URL(context.Context, security.Actor, file.ID) (string, error)
}

type SenderPolicy struct {
	AllowedAddresses []string
	AllowedDomains   []string
}

type RendererConfig struct {
	MaxTemplateLength int
	MaxResultLength   int
	SenderPolicy      SenderPolicy
}

type Renderer struct {
	fields        field.TypeResolver
	files         FileService
	siteID        site.ID
	siteVariables site.TemplateVariables
	config        RendererConfig
}

func NewRenderer(fields field.TypeResolver, files FileService, item site.Site, params []field.Definition, config RendererConfig) (*Renderer, error) {
	if fields == nil {
		return nil, errors.New("mail field type resolver is nil")
	}
	if files == nil {
		return nil, errors.New("mail file service is nil")
	}
	if config.MaxTemplateLength == 0 {
		config.MaxTemplateLength = DefaultMaxTemplateLength
	}
	if config.MaxResultLength == 0 {
		config.MaxResultLength = DefaultMaxResultLength
	}
	if config.MaxTemplateLength < 1 || config.MaxResultLength < 1 {
		return nil, errors.New("mail rendering limits are invalid")
	}
	if len(config.SenderPolicy.AllowedAddresses) == 0 && len(config.SenderPolicy.AllowedDomains) == 0 && item.Domain != "" {
		config.SenderPolicy.AllowedDomains = []string{item.Domain}
	}
	config.SenderPolicy = normalizeSenderPolicy(config.SenderPolicy)
	return &Renderer{fields: fields, files: files, siteID: item.ID, siteVariables: site.NewTemplateVariables(item, params), config: config}, nil
}

func (r *Renderer) ValidateTemplate(template Template) error {
	if template.SiteID <= 0 || template.SiteID != r.siteID || !templateCodePattern.MatchString(template.Code) || strings.TrimSpace(template.Name) == "" {
		return fmt.Errorf("%w: identity is invalid", ErrInvalid)
	}
	if strings.TrimSpace(template.From.Email) == "" {
		return fmt.Errorf("%w: from email is required", ErrInvalid)
	}
	recipients := 0
	for fieldName, addresses := range map[string][]AddressTemplate{
		"to":  template.To,
		"cc":  template.CC,
		"bcc": template.BCC,
	} {
		for index, address := range addresses {
			name := strings.TrimSpace(address.Name)
			email := strings.TrimSpace(address.Email)
			if name == "" && email == "" {
				continue
			}
			if email == "" {
				return fmt.Errorf("%w: %s.%d email is required", ErrInvalid, fieldName, index)
			}
			recipients++
		}
	}
	if recipients == 0 {
		return ErrNoRecipients
	}
	if template.ReplyTo != nil && strings.TrimSpace(template.ReplyTo.Email) == "" {
		return fmt.Errorf("%w: reply_to email is required", ErrInvalid)
	}
	if template.ContentType != ContentText && template.ContentType != ContentHTML {
		return fmt.Errorf("%w: content type is invalid", ErrInvalid)
	}
	definitions := field.CloneDefinitions(template.Variables)
	if _, err := field.Compile(definitions, r.fields); err != nil {
		return fmt.Errorf("%w: variable schema: %v", ErrInvalid, err)
	}
	allowed := allowedVariables(definitions, r.siteVariables)
	sources := templateSources(template)
	for name, source := range sources {
		if _, err := templating.Compile(source, allowed, r.limits()); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalid, name, err)
		}
	}
	fileVariables := make(map[string]struct{})
	for _, definition := range definitions {
		if !supportedVariableType(definition.Type) {
			return fmt.Errorf("%w: variable %q has unsupported type %q", ErrInvalid, definition.Key, definition.Type)
		}
		if definition.Type == field.TypeFile {
			fileVariables["data."+definition.Key] = struct{}{}
		}
	}
	for index, attachment := range template.Attachments {
		switch attachment.Source {
		case AttachmentStatic:
			if attachment.FileID == nil || *attachment.FileID <= 0 || attachment.Variable != "" {
				return fmt.Errorf("%w: attachment %d static source is invalid", ErrInvalid, index)
			}
		case AttachmentVariable:
			if attachment.FileID != nil {
				return fmt.Errorf("%w: attachment %d variable source is invalid", ErrInvalid, index)
			}
			variable := normalizeDataVariable(attachment.Variable)
			if _, exists := fileVariables[variable]; !exists {
				return fmt.Errorf("%w: attachment %d references non-file variable %q", ErrInvalid, index, attachment.Variable)
			}
		case AttachmentSite:
			if attachment.FileID != nil {
				return fmt.Errorf("%w: attachment %d site source is invalid", ErrInvalid, index)
			}
			variable := strings.TrimSpace(attachment.Variable)
			definition, exists := r.siteVariables.Definition(variable)
			if !exists || definition.Type != field.TypeFile {
				return fmt.Errorf("%w: attachment %d references non-file site variable %q", ErrInvalid, index, attachment.Variable)
			}
		default:
			return fmt.Errorf("%w: attachment %d source is invalid", ErrInvalid, index)
		}
	}
	return nil
}

func (r *Renderer) Render(ctx context.Context, template Template, values map[string]any, actor security.Actor) (RenderedMessage, error) {
	if ctx == nil {
		return RenderedMessage{}, errors.New("mail render context is nil")
	}
	if err := r.ValidateTemplate(template); err != nil {
		return RenderedMessage{}, err
	}
	schema, err := field.Compile(template.Variables, r.fields)
	if err != nil {
		return RenderedMessage{}, err
	}
	normalized, err := schema.Validate(values)
	if err != nil {
		return RenderedMessage{}, fmt.Errorf("%w: variable values: %v", ErrInvalid, err)
	}
	fileReferences, err := schema.FileReferences(normalized)
	if err != nil {
		return RenderedMessage{}, fmt.Errorf("%w: file variables: %v", ErrInvalid, err)
	}
	for _, reference := range fileReferences {
		item, loadErr := r.files.GetFile(ctx, actor, file.ID(reference.ID))
		if loadErr != nil {
			return RenderedMessage{}, fmt.Errorf("%w: file variable %q: %v", ErrInvalid, reference.Key, loadErr)
		}
		if !field.FileMatches(reference.Options, item.Storage, item.MIMEType) {
			return RenderedMessage{}, fmt.Errorf("%w: file variable %q violates its file constraints", ErrInvalid, reference.Key)
		}
	}
	allowed := allowedVariables(template.Variables, r.siteVariables)
	warnings := make([]Warning, 0)
	resolve := func(variable string) (any, bool, error) {
		if value, exists := r.siteVariables.Value(variable); exists {
			definition, _ := r.siteVariables.Definition(variable)
			return r.scalarValue(ctx, definition, value)
		}
		key, found := strings.CutPrefix(variable, "data.")
		if !found {
			return nil, false, nil
		}
		value, exists := normalized[key]
		if !exists {
			return nil, false, nil
		}
		definition, exists := definitionByKey(template.Variables, key)
		if !exists {
			return nil, false, nil
		}
		return r.scalarValue(ctx, definition, value)
	}
	render := func(name, source string, context templating.Context) (string, error) {
		compiled, err := templating.Compile(source, allowed, r.limits())
		if err != nil {
			return "", err
		}
		result, err := templating.Render(compiled, resolve, context)
		if err != nil {
			return "", err
		}
		for _, warning := range result.Warnings {
			warnings = append(warnings, Warning{Field: name, Variable: warning.Variable, Message: warning.Message})
		}
		return result.Value, nil
	}

	from, err := r.renderAddress("from", template.From, render)
	if err != nil {
		return RenderedMessage{}, err
	}
	if from == nil {
		return RenderedMessage{}, fmt.Errorf("%w: sender is empty", ErrInvalid)
	}
	if err := r.validateSender(from.Email); err != nil {
		return RenderedMessage{}, err
	}
	to, err := r.renderAddresses("to", template.To, render)
	if err != nil {
		return RenderedMessage{}, err
	}
	cc, err := r.renderAddresses("cc", template.CC, render)
	if err != nil {
		return RenderedMessage{}, err
	}
	bcc, err := r.renderAddresses("bcc", template.BCC, render)
	if err != nil {
		return RenderedMessage{}, err
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		return RenderedMessage{}, ErrNoRecipients
	}
	var replyTo *Address
	if template.ReplyTo != nil {
		replyTo, err = r.renderAddress("reply_to", *template.ReplyTo, render)
		if err != nil {
			return RenderedMessage{}, err
		}
	}
	subject, err := render("subject", template.Subject, templating.Header)
	if err != nil {
		return RenderedMessage{}, fmt.Errorf("%w: subject: %v", ErrInvalid, err)
	}
	result := RenderedMessage{From: *from, To: to, CC: cc, BCC: bcc, ReplyTo: replyTo, Subject: subject, ContentType: template.ContentType}
	if template.ContentType == ContentText {
		result.TextBody, err = render("text_body", template.TextBody, templating.PlainText)
	} else {
		result.HTMLBody, err = render("html_body", template.HTMLBody, templating.HTML)
	}
	if err != nil {
		return RenderedMessage{}, fmt.Errorf("%w: body: %v", ErrInvalid, err)
	}
	result.Attachments, err = r.renderAttachments(ctx, template, normalized, actor, render, &warnings)
	if err != nil {
		return RenderedMessage{}, err
	}
	result.Warnings = warnings
	return result, nil
}

type renderString func(string, string, templating.Context) (string, error)

func (r *Renderer) renderAddress(fieldName string, source AddressTemplate, render renderString) (*Address, error) {
	name, err := render(fieldName+".name", source.Name, templating.Header)
	if err != nil {
		return nil, fmt.Errorf("%w: %s name: %v", ErrInvalid, fieldName, err)
	}
	email, err := render(fieldName+".email", source.Email, templating.Header)
	if err != nil {
		return nil, fmt.Errorf("%w: %s email: %v", ErrInvalid, fieldName, err)
	}
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" && email == "" {
		return nil, nil
	}
	if email == "" || strings.ContainsAny(email, "<>") {
		return nil, fmt.Errorf("%w: %s email is invalid", ErrInvalid, fieldName)
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Address != email {
		return nil, fmt.Errorf("%w: %s email is invalid", ErrInvalid, fieldName)
	}
	return &Address{Name: name, Email: email}, nil
}

func (r *Renderer) renderAddresses(fieldName string, source []AddressTemplate, render renderString) ([]Address, error) {
	result := make([]Address, 0, len(source))
	for index, item := range source {
		address, err := r.renderAddress(fmt.Sprintf("%s.%d", fieldName, index), item, render)
		if err != nil {
			return nil, err
		}
		if address != nil {
			result = append(result, *address)
		}
	}
	return result, nil
}

func (r *Renderer) renderAttachments(ctx context.Context, template Template, values map[string]any, actor security.Actor, render renderString, warnings *[]Warning) ([]Attachment, error) {
	result := make([]Attachment, 0, len(template.Attachments))
	for index, source := range template.Attachments {
		var id file.ID
		accessActor := security.System()
		switch source.Source {
		case AttachmentStatic:
			id = *source.FileID
		case AttachmentVariable:
			accessActor = actor
			key := strings.TrimPrefix(normalizeDataVariable(source.Variable), "data.")
			value, exists := values[key]
			if !exists {
				*warnings = append(*warnings, Warning{Field: fmt.Sprintf("attachments.%d", index), Variable: "data." + key, Message: "variable attachment has no current value"})
				continue
			}
			integer, ok := value.(int64)
			if !ok || integer <= 0 {
				return nil, fmt.Errorf("%w: attachment variable %q is invalid", ErrInvalid, key)
			}
			id = file.ID(integer)
		case AttachmentSite:
			value, exists := r.siteVariables.Value(source.Variable)
			if !exists {
				*warnings = append(*warnings, Warning{Field: fmt.Sprintf("attachments.%d", index), Variable: source.Variable, Message: "site attachment has no current value"})
				continue
			}
			integer, ok := value.(int64)
			if !ok || integer <= 0 {
				return nil, fmt.Errorf("%w: site attachment variable %q is invalid", ErrInvalid, source.Variable)
			}
			id = file.ID(integer)
		}
		item, err := r.files.GetFile(ctx, accessActor, id)
		if err != nil {
			return nil, fmt.Errorf("resolve mail attachment %d: %w", id, err)
		}
		filename := item.Name
		if source.FilenameTemplate != "" {
			filename, err = render(fmt.Sprintf("attachments.%d.filename", index), source.FilenameTemplate, templating.Header)
			if err != nil {
				return nil, fmt.Errorf("%w: attachment filename: %v", ErrInvalid, err)
			}
		}
		filename = strings.TrimSpace(filename)
		if filename == "" || filepath.Base(filename) != filename || filename == "." {
			return nil, fmt.Errorf("%w: attachment filename is invalid", ErrInvalid)
		}
		fileID := item.ID
		result = append(result, Attachment{Source: source.Source, FileID: &fileID, Filename: filename, MIMEType: item.MIMEType, Size: item.Size, Checksum: item.ChecksumSHA256})
	}
	return result, nil
}

func (r *Renderer) ValidateTemplateFiles(ctx context.Context, actor security.Actor, template Template) error {
	for index, attachment := range template.Attachments {
		if attachment.Source != AttachmentStatic || attachment.FileID == nil {
			continue
		}
		if _, err := r.files.GetFile(ctx, actor, *attachment.FileID); err != nil {
			return fmt.Errorf("%w: static attachment %d: %v", ErrInvalid, index, err)
		}
	}
	return nil
}

func (r *Renderer) scalarValue(ctx context.Context, definition field.Definition, value any) (any, bool, error) {
	if definition.Type == field.TypeFile {
		id, ok := value.(int64)
		if !ok || id <= 0 {
			return nil, false, nil
		}
		url, err := r.files.URL(ctx, security.System(), file.ID(id))
		if err != nil {
			return nil, false, fmt.Errorf("file %d has no safe public URL: %w", id, err)
		}
		return url, true, nil
	}
	switch current := value.(type) {
	case []string:
		return strings.Join(current, ", "), len(current) > 0, nil
	case []any:
		items := make([]string, 0, len(current))
		for _, item := range current {
			text, ok := item.(string)
			if !ok {
				return nil, false, fmt.Errorf("unsupported list value %T", item)
			}
			items = append(items, text)
		}
		return strings.Join(items, ", "), len(items) > 0, nil
	case string:
		return current, strings.TrimSpace(current) != "", nil
	default:
		return current, true, nil
	}
}

func (r *Renderer) limits() templating.Limits {
	return templating.Limits{MaxSourceLength: r.config.MaxTemplateLength, MaxResultLength: r.config.MaxResultLength}
}

func (r *Renderer) validateSender(address string) error {
	policy := r.config.SenderPolicy
	lower := strings.ToLower(address)
	for _, allowed := range policy.AllowedAddresses {
		if lower == allowed {
			return nil
		}
	}
	_, domain, found := strings.Cut(lower, "@")
	if found {
		for _, allowed := range policy.AllowedDomains {
			if domain == allowed {
				return nil
			}
		}
	}
	return ErrSenderNotAllowed
}

func normalizeSenderPolicy(policy SenderPolicy) SenderPolicy {
	for index := range policy.AllowedAddresses {
		policy.AllowedAddresses[index] = strings.ToLower(strings.TrimSpace(policy.AllowedAddresses[index]))
	}
	for index := range policy.AllowedDomains {
		policy.AllowedDomains[index] = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(policy.AllowedDomains[index]), "@"))
	}
	sort.Strings(policy.AllowedAddresses)
	sort.Strings(policy.AllowedDomains)
	return policy
}

func allowedVariables(definitions []field.Definition, siteVariables site.TemplateVariables) map[string]struct{} {
	result := siteVariables.Allowed()
	for _, definition := range definitions {
		result["data."+definition.Key] = struct{}{}
	}
	return result
}

func (r *Renderer) SiteVariables() []site.TemplateVariable {
	return r.siteVariables.Metadata()
}

func definitionByKey(definitions []field.Definition, key string) (field.Definition, bool) {
	for _, definition := range definitions {
		if definition.Key == key {
			return definition, true
		}
	}
	return field.Definition{}, false
}

func normalizeDataVariable(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "data.") {
		return value
	}
	return "data." + value
}

func supportedVariableType(code field.TypeCode) bool {
	switch code {
	case field.TypeString, field.TypeInteger, field.TypeFloat, field.TypeCheckbox,
		field.TypeRadio, field.TypeSelect, field.TypeTextarea, field.TypeEmail,
		field.TypePhone, field.TypeFile:
		return true
	default:
		return false
	}
}

func templateSources(template Template) map[string]string {
	result := map[string]string{
		"from.name": template.From.Name, "from.email": template.From.Email,
		"subject": template.Subject, "text_body": template.TextBody, "html_body": template.HTMLBody,
	}
	for index, address := range template.To {
		result[fmt.Sprintf("to.%d.name", index)] = address.Name
		result[fmt.Sprintf("to.%d.email", index)] = address.Email
	}
	for index, address := range template.CC {
		result[fmt.Sprintf("cc.%d.name", index)] = address.Name
		result[fmt.Sprintf("cc.%d.email", index)] = address.Email
	}
	for index, address := range template.BCC {
		result[fmt.Sprintf("bcc.%d.name", index)] = address.Name
		result[fmt.Sprintf("bcc.%d.email", index)] = address.Email
	}
	if template.ReplyTo != nil {
		result["reply_to.name"] = template.ReplyTo.Name
		result["reply_to.email"] = template.ReplyTo.Email
	}
	for index, attachment := range template.Attachments {
		result[fmt.Sprintf("attachments.%d.filename", index)] = attachment.FilenameTemplate
	}
	return result
}
