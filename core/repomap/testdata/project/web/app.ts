export interface AppConfig {
    name: string;
}

export function createApp(cfg: AppConfig): AppConfig {
    return cfg;
}
