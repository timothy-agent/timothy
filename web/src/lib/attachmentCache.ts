// Object URLs fetched per attachment id, cached module-level: an
// attachment is content-addressed and immutable (D-045), so replaying
// the same transcript (reload, resumed session) or reopening the
// viewer for a thumbnail already shown never refetches it. Shared by
// Message.tsx (thumbnails) and AttachmentViewer.tsx (the fullscreen
// modal) so the two never double-fetch the same id.
export const attachmentURLCache = new Map<string, string>()
