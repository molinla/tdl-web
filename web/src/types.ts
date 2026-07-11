export type MediaType = "video" | "image" | "file";
export type ItemStatus =
  | "queued"
  | "caching"
  | "paused"
  | "completed"
  | "error"
  | "skipped";

export interface Item {
  id: string;
  peer_id: number;
  message_id: number;
  logical_pos: number;
  name: string;
  mime: string;
  type: MediaType;
  size: number;
  duration?: number;
  date?: number;
  status: ItemStatus;
  progress: number;
  error?: string;
  target_path: string;
  thumb_url?: string;
  cover?: string;
  preview_url?: string;
  stream_url?: string;
  download_url: string;
  resume_completed: boolean;
  skip_same: boolean;
  queue_pos?: number;
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
}

export type RangeType = "" | "id" | "time";
