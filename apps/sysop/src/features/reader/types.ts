export type ReaderRendererKind =
  | 'article'
  | 'image'
  | 'gallery'
  | 'video'
  | 'audio'
  | 'document'
  | 'text'
  | 'unknown'

export type MediaKind = 'image' | 'video' | 'audio' | 'document' | 'timed_text' | 'other'

export type VariantKind =
  | 'original'
  | 'preview'
  | 'thumbnail'
  | 'poster'
  | 'audio'
  | 'subtitles'
  | 'transcript'

export type AcquisitionState = 'pending' | 'available' | 'reference_only' | 'failed'

export interface ReaderResolvedText {
  value: string
  source: string
  observation_id?: string
}

export interface ReaderDisplay {
  title: ReaderResolvedText
  description?: ReaderResolvedText
  byline?: ReaderResolvedText
  published_at?: string
  summary: ReaderResolvedText
}

export interface ReaderArticleContent {
  preview_markdown: string
  full_content_available: boolean
  full_content_href?: string
}

export interface ReaderAssetFailure {
  code: string
  message: string
  retryable: boolean
}

export interface ReaderAssetVariant {
  asset_variant_id: string
  kind: VariantKind
  custody: 'reference' | 'cache' | 'mirror' | 'adopted'
  acquisition_state: AcquisitionState
  mime_type?: string
  width?: number
  height?: number
  duration_seconds?: number
  byte_size?: number
  content_href?: string
  source_url?: string
  failure?: ReaderAssetFailure
}

export interface ReaderAttachmentRef {
  attachment_id: string
  fragment_revision_id: string
  media_asset_id: string
  role: 'primary' | 'gallery_item' | 'hero' | 'inline' | 'poster' | 'transcript' | 'other'
  position: number
  caption?: string
  source_context?: string
}

export interface ReaderMediaItem {
  attachment: ReaderAttachmentRef
  media_asset_id: string
  provider_media_id?: string
  kind: MediaKind
  alt_text?: string
  variants: ReaderAssetVariant[]
}

export interface ReaderPlaybackSpec {
  kind: string
  provider?: string
  provider_item_id?: string
  asset_variant_id?: string
  url?: string
  mime_type?: string
  policy?: string
  start_seconds?: number
}

/**
 * The renderer-owned, frozen-contract-compatible portion of ReaderItem.
 * A complete ReaderItem is structurally assignable without an adapter.
 */
export interface ReaderRenderableItem {
  fragment_id: string
  fragment_revision_id: string
  renderer: ReaderRendererKind | string
  display: ReaderDisplay
  article: ReaderArticleContent
  media: ReaderMediaItem[]
  playback?: ReaderPlaybackSpec
  tags?: {
    combined: string[]
  }
}

export type ReaderPresentation = 'card' | 'detail'

export interface ReaderRendererProps {
  item: ReaderRenderableItem
  presentation: ReaderPresentation
  className?: string
}
