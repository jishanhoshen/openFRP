export interface Preset {
  id: string;
  label: string;
  type: "tcp" | "udp";
  port: number | "";
  name: string;
  hint: string;
}

/** Starting points for the games and servers people most often share. */
export const PRESETS: Preset[] = [
  { id: "mc-java", label: "Minecraft (Java)", type: "tcp", port: 25565, name: "minecraft",
    hint: "Players type only your address. Minecraft's default port is 25565." },
  { id: "mc-bedrock", label: "Minecraft (Bedrock)", type: "udp", port: 19132, name: "minecraft-bedrock",
    hint: "Bedrock and Pocket Edition use UDP on port 19132." },
  { id: "udp-game", label: "UDP game", type: "udp", port: 26001, name: "game",
    hint: "Use the local port your game server listens on." },
  { id: "tcp-app", label: "Web or TCP app", type: "tcp", port: 8080, name: "app",
    hint: "Any TCP service running on this computer." },
  { id: "custom", label: "Custom", type: "tcp", port: "", name: "", hint: "Choose your own protocol and port." },
];

export const NAME_RE = /^[a-z0-9][a-z0-9-]{0,47}$/;
