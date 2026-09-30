#version 330

in vec2 fragTexCoord;
in vec4 fragColor;
out vec4 finalColor;

uniform sampler2D texture0;
uniform sampler2D depthMap;
uniform vec4 colDiffuse;

float heightAt(vec2 uv, float fallbackHeight) {
    vec4 sampleDepth = texture(depthMap, uv);
    if (sampleDepth.b < 0.5) return fallbackHeight;
    return dot(round(sampleDepth.rg * 255.0), vec2(256.0, 1.0)) - 32768.0;
}

void main() {
    vec4 color = texture(texture0, fragTexCoord) * colDiffuse * fragColor;
    if (texture(depthMap, fragTexCoord).b < 0.5) {
        finalColor = color;
        return;
    }
    vec2 texel = 1.0 / vec2(textureSize(depthMap, 0));
    float center = heightAt(fragTexCoord, 0.0);
    // Each pixel spans one block along world X/Z. Empty neighbors use the
    // center height, and texture clamping provides a safe region-edge fallback.
    float dx = (heightAt(fragTexCoord + vec2(texel.x, 0), center)
              - heightAt(fragTexCoord - vec2(texel.x, 0), center)) * 0.5;
    float dz = (heightAt(fragTexCoord + vec2(0, texel.y), center)
              - heightAt(fragTexCoord - vec2(0, texel.y), center)) * 0.5;
    vec3 normal = normalize(vec3(-dx, 1.0, -dz));
    // Northwest light, above the terrain. Ambient light keeps cliffs readable.
    vec3 light = normalize(vec3(-1.0, 1.5, -1.0));
    float shade = 0.45 + 0.55 * max(dot(normal, light), 0.0);
    finalColor = vec4(color.rgb * shade, color.a);
}
