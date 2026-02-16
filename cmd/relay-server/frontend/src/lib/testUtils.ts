import type { ClientServer } from "@/types/server";

// Generate random sample servers
export const generateRandomServers = (
  count: number,
  startId: number = 1
): ClientServer[] => {
  const serverNames = [
    "Atlas Network",
    "Phoenix Hub",
    "Nebula Station",
    "Quantum Gateway",
    "Crystal Core",
    "Thunder Relay",
    "Skyline Portal",
    "Velocity Node",
    "Horizon Link",
    "Apex Server",
    "Nova Cluster",
    "Titan Network",
    "Eclipse Gateway",
    "Zenith Hub",
    "Aurora Station",
    "Pulse Center",
    "Matrix Core",
    "Frontier Node",
    "Summit Link",
    "Vortex Portal",
    "Cipher Network",
    "Nexus Hub",
    "Prism Station",
    "Radiant Gateway",
    "Omega Core",
    "Spectrum Relay",
    "Cascade Portal",
    "Infinity Node",
  ];

  const descriptions = [
    "High-performance gaming server with low latency",
    "Community hub for developers and creators",
    "Private cloud infrastructure for enterprises",
    "Media streaming and content delivery platform",
    "Real-time collaboration workspace",
    "Secure data processing and analytics",
    "Educational platform for online learning",
    "Social network for creative professionals",
    "E-commerce platform with global reach",
    "Healthcare data management system",
    "Financial trading and analytics hub",
    "IoT device management platform",
    "AI/ML model training infrastructure",
    "Video conferencing and webinar service",
    "Open source project hosting",
    "Blockchain node and validator",
  ];

  const tagOptions = [
    "Gaming",
    "Development",
    "Cloud",
    "Media",
    "Collaboration",
    "Analytics",
    "Education",
    "Social",
    "Commerce",
    "Healthcare",
    "Finance",
    "IoT",
    "AI/ML",
    "Video",
    "Open Source",
    "Blockchain",
    "Security",
    "High-Performance",
    "Low-Latency",
    "Global",
  ];

  const owners = [
    "TechCorp",
    "DevTeam",
    "CloudNet",
    "MediaHub",
    "DataLabs",
    "SecureOps",
    "GlobalTech",
    "InnovateCo",
    "NextGen",
    "AlphaSystems",
    "BetaWorks",
    "GammaNet",
    "DeltaCloud",
    "EpsilonLabs",
    "ZetaTech",
  ];

  const thumbnails = [
    "https://images.unsplash.com/photo-1558494949-ef010cbdcc31?w=400",
    "https://images.unsplash.com/photo-1451187580459-43490279c0fa?w=400",
    "https://images.unsplash.com/photo-1526374965328-7f61d4dc18c5?w=400",
    "https://images.unsplash.com/photo-1550751827-4bd374c3f58b?w=400",
    "https://images.unsplash.com/photo-1504384308090-c894fdcc538d?w=400",
    "https://images.unsplash.com/photo-1518770660439-4636190af475?w=400",
    "https://images.unsplash.com/photo-1488590528505-98d2b5aba04b?w=400",
    "https://images.unsplash.com/photo-1461749280684-dccba630e2f6?w=400",
  ];

  return Array.from({ length: count }, (_, i) => {
    const id = startId + i;
    const online = Math.random() > 0.3; // 70% online
    const numTags = Math.floor(Math.random() * 4) + 1; // 1-4 tags
    const selectedTags = Array.from(
      { length: numTags },
      () => tagOptions[Math.floor(Math.random() * tagOptions.length)]
    ).filter((tag, index, self) => self.indexOf(tag) === index); // Remove duplicates

    return {
      id,
      name:
        serverNames[Math.floor(Math.random() * serverNames.length)] + ` #${id}`,
      description:
        descriptions[Math.floor(Math.random() * descriptions.length)],
      tags: selectedTags,
      thumbnail: thumbnails[Math.floor(Math.random() * thumbnails.length)],
      owner: owners[Math.floor(Math.random() * owners.length)],
      online,
      dns: `server-${id}.example.com`,
      link: `https://server-${id}.example.com`,
      lastUpdated: new Date(
        Date.now() - Math.random() * 30 * 24 * 60 * 60 * 1000
      ).toISOString(),
    };
  });
};
