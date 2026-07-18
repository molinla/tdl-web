export type MediaType = "video" | "image" | "file" | "message";
export type ItemStatus =
  | "queued"
  | "caching"
  | "paused"
  | "completed"
  | "error"
  | "skipped";

export interface Item {
  id: string;
  chat_id?: string;
  peer_id: number;
  message_id: number;
  logical_pos: number;
  name: string;
  mime: string;
  type: MediaType;
  size: number;
  duration?: number;
  date?: number;
  message_kind?: string;
  text?: string;
  author?: string;
  forwarded_from?: string;
  saved_from?: string;
  media_unavailable?: string;
  status: ItemStatus;
  progress: number;
  error?: string;
  target_path: string;
  thumb_url?: string;
  cover?: string;
  cover_aspect?: number;
  preview_url?: string;
  stream_url?: string;
  download_url?: string;
  resume_completed: boolean;
  skip_same: boolean;
  queue_pos?: number;
  manual_paused?: boolean;
}

export interface ItemsPayload {
  fingerprint?: string;
  items: Item[];
  importing: boolean;
  import_error?: string;
  import_total?: number;
  import_done?: number;
  import_items?: number;
  import_phase?: string;
  import_source?: string;
  import_detail?: string;
  downloading_count?: number;
  queued_count?: number;
  cover_building_count?: number;
  cover_queued_count?: number;
  active_chat?: string;
  active_chat_title?: string;
  chat_has_more?: boolean;
}

export interface ProgressPayload {
  items: Array<{
    id: string;
    progress: number;
  }>;
}

export type RangeType = "" | "id" | "time";

export interface ChatInfo {
  id: string;
  title: string;
  type: "saved" | "private" | "group" | "channel";
}
