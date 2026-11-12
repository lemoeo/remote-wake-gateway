export interface Device {
  id: string; name: string; type: string; enabled: boolean; revision: number; wake_method: string; ip: string; note: string;
  wol: {mac: string; broadcast: string; interface: string; port: number; repeat: number; interval_ms: number};
  vnc: {port: number; username: string; hold_seconds: number}; probe: {enabled: boolean; port: number; timeout_seconds: number};
  password_configured?: boolean; active_job_id?: string
}
export interface Job { job_id: string; command_id: string; device_id: string; action: string; status: string; phase: string; message?: string; created_at: string; expires_at?: string; finished_at?: string; steps: {name: string; status: string; code: string; message: string}[]; observation: {state: string; observed_at?: string} }

export interface HistoryCleanup {scope: string; before: string; retention_days: number; eligible: number; active: number; protected: number; deleted: number}
export interface Settings {schema_version: number; gateway_id: string; name: string; listen: string; public_url: string; allowed_networks: string[]; max_concurrent: number; max_vnc: number; queue_size: number; retention_days: number; max_jobs: number; device_defaults: DeviceDefaults}

export interface DeviceDefaults {broadcast:string;port:number;repeat:number}
export interface BatchRow {device_id:string;name:string;command_id:string;job_id?:string;status:string;message:string}
