export interface CronJob {
  id: string;
  owner_id: string;
  agent_id: string;
  agent_name: string;
  model: string;
  expression: string;
  timezone: string;
  description: string;
  prompt: string;
  enabled: boolean;
  notifications_enabled: boolean;
  deliver_to_bot: boolean;
  bot_id: string;
  disabled_reason?: string;
  last_run_at?: string;
  next_run_at?: string;
  last_conversation_id?: string;
  last_error?: string;
  created_at: string;
  updated_at: string;
}

export interface CronJobInput {
  agent_id: string;
  model: string;
  expression: string;
  timezone: string;
  description: string;
  prompt: string;
  enabled: boolean;
  notifications_enabled: boolean;
  deliver_to_bot: boolean;
  bot_id: string;
}
